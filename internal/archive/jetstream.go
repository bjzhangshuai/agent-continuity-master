package archive

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
)

type BrokerConfig struct {
	URL             string `json:"url"`
	Stream          string `json:"stream"`
	Subject         string `json:"subject"`
	Consumer        string `json:"consumer"`
	Replicas        int    `json:"replicas"`
	MaxBytes        int64  `json:"max_bytes"`
	MaxMessages     int64  `json:"max_messages"`
	TokenFile       string `json:"token_file,omitempty"`
	Username        string `json:"username,omitempty"`
	PasswordFile    string `json:"password_file,omitempty"`
	CredentialsFile string `json:"credentials_file,omitempty"`
	CAFile          string `json:"ca_file,omitempty"`
	ClientCertFile  string `json:"client_cert_file,omitempty"`
	ClientKeyFile   string `json:"client_key_file,omitempty"`
	AllowInsecure   bool   `json:"allow_insecure,omitempty"`
}

func (c BrokerConfig) Validate() error {
	u, err := url.Parse(c.URL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return errors.New("broker URL must be a NATS origin without embedded credentials")
	}
	if u.Scheme != "tls" && !(u.Scheme == "nats" && c.AllowInsecure) {
		return errors.New("broker requires tls://; allow_insecure is for an isolated development network only")
	}
	name := regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)
	subject := regexp.MustCompile(`^[A-Za-z0-9_-]+(\.[A-Za-z0-9_-]+)*$`)
	if !name.MatchString(c.Stream) || !name.MatchString(c.Consumer) || !subject.MatchString(c.Subject) {
		return errors.New("invalid broker stream, consumer or subject")
	}
	if c.Replicas != 1 && c.Replicas != 3 && c.Replicas != 5 {
		return errors.New("broker stream replicas must be 1, 3 or 5")
	}
	if c.MaxBytes < 1<<20 || c.MaxMessages < 1 {
		return errors.New("broker capacity is too small")
	}
	authMethods := 0
	for _, value := range []string{c.TokenFile, c.CredentialsFile, c.PasswordFile} {
		if value != "" {
			authMethods++
		}
	}
	if authMethods > 1 {
		return errors.New("choose token_file, credentials_file, or username/password_file")
	}
	if (c.Username == "") != (c.PasswordFile == "") {
		return errors.New("broker username and password_file must be paired")
	}
	if (c.ClientCertFile == "") != (c.ClientKeyFile == "") {
		return errors.New("broker TLS client certificate and key must be paired")
	}
	return nil
}

type JetStream struct {
	connection   *nats.Conn
	js           nats.JetStreamContext
	subscription *nats.Subscription
	config       BrokerConfig
}

func ConnectBroker(c BrokerConfig, timeout time.Duration) (*JetStream, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	opts := []nats.Option{nats.Name("agent-continuity-archive"), nats.Timeout(5 * time.Second), nats.MaxReconnects(-1), nats.ReconnectWait(2 * time.Second), nats.ReconnectBufSize(0)}
	if c.TokenFile != "" {
		b, err := os.ReadFile(c.TokenFile)
		if err != nil {
			return nil, errors.New("broker token file unavailable")
		}
		token := strings.TrimSpace(string(b))
		if token == "" {
			return nil, errors.New("empty broker token")
		}
		opts = append(opts, nats.Token(token))
	}
	if c.CredentialsFile != "" {
		opts = append(opts, nats.UserCredentials(c.CredentialsFile))
	}
	if c.PasswordFile != "" {
		b, err := os.ReadFile(c.PasswordFile)
		if err != nil {
			return nil, errors.New("broker password file unavailable")
		}
		password := strings.TrimRight(string(b), "\r\n")
		if password == "" {
			return nil, errors.New("empty broker password")
		}
		opts = append(opts, nats.UserInfo(c.Username, password))
	}
	if c.CAFile != "" {
		opts = append(opts, nats.RootCAs(c.CAFile))
	}
	if c.ClientCertFile != "" {
		opts = append(opts, nats.ClientCert(c.ClientCertFile, c.ClientKeyFile))
	}
	nc, err := nats.Connect(c.URL, opts...)
	if err != nil {
		return nil, errors.New("broker connection unavailable")
	}
	ok := false
	defer func() {
		if !ok {
			nc.Close()
		}
	}()
	js, err := nc.JetStream(nats.MaxWait(5 * time.Second))
	if err != nil {
		return nil, err
	}
	info, err := js.StreamInfo(c.Stream)
	if errors.Is(err, nats.ErrStreamNotFound) {
		info, err = js.AddStream(&nats.StreamConfig{Name: c.Stream, Subjects: []string{c.Subject}, Retention: nats.WorkQueuePolicy, Storage: nats.FileStorage, Replicas: c.Replicas, MaxBytes: c.MaxBytes, MaxMsgs: c.MaxMessages, Discard: nats.DiscardNew, Duplicates: 2 * time.Minute})
		if err != nil {
			info, err = js.StreamInfo(c.Stream)
		} // concurrent provisioner may have won
	}
	if err != nil {
		return nil, errors.New("broker stream unavailable")
	}
	if info.Config.Storage != nats.FileStorage || info.Config.Retention != nats.WorkQueuePolicy || info.Config.Replicas != c.Replicas || info.Config.Discard != nats.DiscardNew || len(info.Config.Subjects) != 1 || info.Config.Subjects[0] != c.Subject || info.Config.MaxAge != 0 || info.Config.MaxBytes != c.MaxBytes || info.Config.MaxMsgs != c.MaxMessages {
		return nil, errors.New("existing broker stream differs from required durable configuration; migrate it explicitly")
	}
	consumer, err := js.ConsumerInfo(c.Stream, c.Consumer)
	ackWait := 2*timeout + 30*time.Second
	if errors.Is(err, nats.ErrConsumerNotFound) {
		consumer, err = js.AddConsumer(c.Stream, &nats.ConsumerConfig{Durable: c.Consumer, AckPolicy: nats.AckExplicitPolicy, AckWait: ackWait, MaxDeliver: -1, FilterSubject: c.Subject, MaxAckPending: 256, MaxWaiting: 512})
		if err != nil {
			consumer, err = js.ConsumerInfo(c.Stream, c.Consumer)
		}
	}
	if err != nil {
		return nil, errors.New("broker durable consumer unavailable")
	}
	if consumer.Config.AckPolicy != nats.AckExplicitPolicy || consumer.Config.DeliverSubject != "" || consumer.Config.FilterSubject != c.Subject || consumer.Config.AckWait < ackWait || consumer.Config.MaxDeliver != -1 {
		return nil, errors.New("existing consumer has incompatible acknowledgement settings")
	}
	sub, err := js.PullSubscribe(c.Subject, c.Consumer, nats.Bind(c.Stream, c.Consumer))
	if err != nil {
		return nil, err
	}
	ok = true
	return &JetStream{nc, js, sub, c}, nil
}
func (b *JetStream) Close() { b.connection.Close() }
func (b *JetStream) Publish(ctx context.Context, j Job) error {
	m := nats.NewMsg(b.config.Subject)
	m.Data = []byte(strconv.FormatInt(j.ID, 10))
	m.Header.Set(nats.MsgIdHdr, fmt.Sprintf("archive/%d/%d", j.ID, j.LeaseVersion))
	_, err := b.js.PublishMsg(m, nats.Context(ctx))
	return err // PubAck is the durability boundary
}
func (w *Worker) RelayOnce(ctx context.Context, b *JetStream) error {
	jobs, err := w.Queue.PendingDispatch(ctx, 100)
	if err != nil {
		return err
	}
	for _, j := range jobs {
		if err = b.Publish(ctx, j); err != nil {
			return err
		}
		if err = w.Queue.MarkDispatched(ctx, j); err != nil {
			return err
		}
	}
	return nil
}
func (w *Worker) Run(ctx context.Context) {
	for ctx.Err() == nil {
		b, err := ConnectBroker(w.Config.Broker, time.Duration(w.Config.TimeoutSeconds)*time.Second)
		if err != nil {
			w.Logger.Warn("archive broker unavailable; uploads remain in transactional outbox")
			if !pause(ctx, 5*time.Second) {
				return
			}
			continue
		}
		w.runConnected(ctx, b)
		b.Close()
		return
	}
}
func (w *Worker) runConnected(ctx context.Context, b *JetStream) {
	var group sync.WaitGroup
	group.Go(func() {
		for ctx.Err() == nil {
			if err := w.RelayOnce(ctx, b); err != nil && ctx.Err() == nil {
				w.Logger.Warn("archive outbox publish deferred")
			}
			if !pause(ctx, time.Duration(w.Config.PollSeconds)*time.Second) {
				return
			}
		}
	})
	for range w.Config.Concurrency {
		group.Go(func() {
			for ctx.Err() == nil {
				messages, err := b.subscription.Fetch(1, nats.MaxWait(time.Second))
				if err != nil {
					if !errors.Is(err, nats.ErrTimeout) {
						pause(ctx, time.Second)
					}
					continue
				}
				for _, m := range messages {
					id, err := strconv.ParseInt(string(m.Data), 10, 64)
					if err != nil || id < 1 {
						_ = m.Term()
						continue
					}
					worked, err := w.ProcessJob(ctx, id)
					if err == nil && !worked {
						j, getErr := w.Queue.Get(ctx, id)
						if errors.Is(getErr, ErrNotFound) || (getErr == nil && (j.State == "stored" || j.State == "dead")) {
							_ = m.AckSync()
							continue
						}
						_ = m.NakWithDelay(5 * time.Second)
						continue
					}
					if err != nil {
						_ = m.NakWithDelay(5 * time.Second)
						continue
					}
					// Both successful archive and persisted retry/dead-letter transitions
					// are committed before ACK. The outbox publishes the next attempt.
					_ = m.AckSync()
				}
			}
		})
	}
	group.Wait()
}
func pause(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
