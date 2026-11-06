package objectstore

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
	osscredentials "github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss/credentials"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	cos "github.com/tencentyun/cos-go-sdk-v5"
)

type s3Provider struct {
	client *minio.Client
	bucket string
}

func newS3(t Target) (Provider, error) {
	u, _ := url.Parse(t.Endpoint)
	lookup := minio.BucketLookupAuto
	if t.PathStyle {
		lookup = minio.BucketLookupPath
	}
	options := &minio.Options{Secure: u.Scheme == "https", Region: t.Region, Creds: credentials.NewStaticV4(t.AccessKey, t.SecretKey, t.SessionToken), BucketLookup: lookup}
	if t.CAFile != "" {
		pem, err := os.ReadFile(t.CAFile)
		if err != nil {
			return nil, errors.New("cannot read object storage CA file")
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(pem) {
			return nil, errors.New("object storage CA file contains no certificates")
		}
		transport, err := minio.DefaultTransport(true)
		if err != nil {
			return nil, err
		}
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
		options.Transport = transport
	}
	c, err := minio.New(u.Host, options)
	if err != nil {
		return nil, err
	}
	return &s3Provider{c, t.Bucket}, nil
}
func (p *s3Provider) Put(ctx context.Context, o Object, r io.Reader) error {
	_, err := p.client.PutObject(ctx, p.bucket, o.Key, r, o.Size, minio.PutObjectOptions{ContentType: o.ContentType, UserMetadata: map[string]string{"sha256": o.SHA256}, DisableMultipart: true})
	return err
}
func (p *s3Provider) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	return p.client.GetObject(ctx, p.bucket, key, minio.GetObjectOptions{})
}
func (p *s3Provider) Delete(ctx context.Context, key string) error {
	return p.client.RemoveObject(ctx, p.bucket, key, minio.RemoveObjectOptions{})
}

type ossProvider struct {
	client *oss.Client
	bucket string
}

func newOSS(t Target) (Provider, error) {
	cfg := oss.LoadDefaultConfig().WithRegion(t.Region).WithEndpoint(t.Endpoint).WithUsePathStyle(t.PathStyle).WithCredentialsProvider(osscredentials.NewStaticCredentialsProvider(t.AccessKey, t.SecretKey, t.SessionToken))
	return &ossProvider{oss.NewClient(cfg), t.Bucket}, nil
}
func (p *ossProvider) Put(ctx context.Context, o Object, r io.Reader) error {
	_, err := p.client.PutObject(ctx, &oss.PutObjectRequest{Bucket: &p.bucket, Key: &o.Key, Body: r, ContentLength: &o.Size, ContentType: &o.ContentType, Metadata: map[string]string{"sha256": o.SHA256}})
	return err
}
func (p *ossProvider) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	r, err := p.client.GetObject(ctx, &oss.GetObjectRequest{Bucket: &p.bucket, Key: &key})
	if err != nil {
		return nil, err
	}
	return r.Body, nil
}

type cosProvider struct{ client *cos.Client }

func (p *ossProvider) Delete(ctx context.Context, key string) error {
	_, err := p.client.DeleteObject(ctx, &oss.DeleteObjectRequest{Bucket: &p.bucket, Key: &key})
	return err
}

func newCOS(t Target) (Provider, error) {
	u, _ := url.Parse(t.Endpoint) // COS endpoint includes the full bucket-appid hostname.
	c := cos.NewClient(&cos.BaseURL{BucketURL: u}, &http.Client{Timeout: 90 * time.Second, Transport: &cos.AuthorizationTransport{SecretID: t.AccessKey, SecretKey: t.SecretKey, SessionToken: t.SessionToken}})
	return &cosProvider{c}, nil
}
func (p *cosProvider) Put(ctx context.Context, o Object, r io.Reader) error {
	res, err := p.client.Object.Put(ctx, o.Key, r, &cos.ObjectPutOptions{ObjectPutHeaderOptions: &cos.ObjectPutHeaderOptions{ContentType: o.ContentType, ContentLength: o.Size, XCosMetaXXX: &http.Header{"X-Cos-Meta-Sha256": []string{o.SHA256}}}})
	if res != nil && res.Body != nil {
		_ = res.Body.Close()
	}
	return err
}
func (p *cosProvider) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	r, err := p.client.Object.Get(ctx, key, nil)
	if err != nil {
		if r != nil && r.Body != nil {
			_ = r.Body.Close()
		}
		return nil, err
	}
	return r.Body, nil
}

type azureProvider struct {
	client    *azblob.Client
	container string
}

func (p *cosProvider) Delete(ctx context.Context, key string) error {
	r, err := p.client.Object.Delete(ctx, key)
	if r != nil && r.Body != nil {
		r.Body.Close()
	}
	if r != nil && r.StatusCode == 404 {
		return nil
	}
	return err
}

func newAzure(t Target) (Provider, error) {
	credential, err := azblob.NewSharedKeyCredential(t.AccountName, t.SecretKey)
	if err != nil {
		return nil, err
	}
	c, err := azblob.NewClientWithSharedKeyCredential(t.Endpoint, credential, nil)
	if err != nil {
		return nil, err
	}
	return &azureProvider{c, t.Bucket}, nil
}
func (p *azureProvider) Put(ctx context.Context, o Object, r io.Reader) error {
	_, err := p.client.UploadStream(ctx, p.container, o.Key, r, &azblob.UploadStreamOptions{BlockSize: 4 << 20, Concurrency: 1, HTTPHeaders: &blob.HTTPHeaders{BlobContentType: &o.ContentType}, Metadata: map[string]*string{"sha256": &o.SHA256}})
	return err
}
func (p *azureProvider) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	r, err := p.client.DownloadStream(ctx, p.container, key, nil)
	if err != nil {
		return nil, err
	}
	return r.Body, nil
}
func (p *azureProvider) Delete(ctx context.Context, key string) error {
	_, err := p.client.DeleteBlob(ctx, p.container, key, nil)
	var response *azcore.ResponseError
	if errors.As(err, &response) && response.StatusCode == 404 {
		return nil
	}
	return err
}
