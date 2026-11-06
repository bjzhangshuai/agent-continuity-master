package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/agent-continuity/agent-continuity-master/internal/secrets"
)

type Config struct {
	WorkflowConfigJSON   string
	KMSConfigJSON        string
	ArchiveConfigJSON    string
	PaymentConfigJSON    string
	TreasuryConfigJSON   string
	BillingPriceBookJSON string
	ListenAddress        string
	Store                string
	DatabaseURL          string
	ArtifactDir          string
	ArtifactBackend      string
	S3Endpoint           string
	S3Bucket             string
	S3Region             string
	S3AccessKey          string
	S3SecretKey          string
	S3SessionToken       string
	S3AutoCreate         bool
	JWTSecret            string
	BootstrapToken       string
	MasterKey            string
	OTLPEndpoint         string
	ServiceName          string
	DataResidency        string
	OIDCIssuer           string
	OIDCAudience         string
	OIDCJWKS             string
	OIDCOrganizationID   string
	OIDCAutoProvision    bool
	SCIMBearerToken      string
	SCIMOrganizationID   string
	AutoMigrate          bool
	ShutdownGrace        time.Duration
	MaxBodyBytes         int64
}

func Load() (Config, error) {
	workflowConfig, err := envOrFile("CONTINUITY_WORKFLOW_CONFIG", "CONTINUITY_WORKFLOW_CONFIG_FILE")
	if err != nil {
		return Config{}, err
	}
	kmsConfig, err := envOrFile("CONTINUITY_KMS_CONFIG", "CONTINUITY_KMS_CONFIG_FILE")
	if err != nil {
		return Config{}, err
	}
	archiveConfig, err := envOrFile("CONTINUITY_ARCHIVE_CONFIG", "CONTINUITY_ARCHIVE_CONFIG_FILE")
	if err != nil {
		return Config{}, err
	}
	treasuryConfig, err := envOrFile("CONTINUITY_TREASURY_CONFIG", "CONTINUITY_TREASURY_CONFIG_FILE")
	if err != nil {
		return Config{}, err
	}
	paymentConfig, err := envOrFile("CONTINUITY_PAYMENT_CONFIG", "CONTINUITY_PAYMENT_CONFIG_FILE")
	if err != nil {
		return Config{}, err
	}
	priceBook, err := envOrFile("CONTINUITY_BILLING_PRICE_BOOK", "CONTINUITY_BILLING_PRICE_BOOK_FILE")
	if err != nil {
		return Config{}, err
	}
	databaseURL, err := envOrFile("CONTINUITY_DATABASE_URL", "CONTINUITY_DATABASE_URL_FILE")
	if err != nil {
		return Config{}, err
	}
	s3AccessKey, err := envOrFile("CONTINUITY_S3_ACCESS_KEY", "CONTINUITY_S3_ACCESS_KEY_FILE")
	if err != nil {
		return Config{}, err
	}
	s3SecretKey, err := envOrFile("CONTINUITY_S3_SECRET_KEY", "CONTINUITY_S3_SECRET_KEY_FILE")
	if err != nil {
		return Config{}, err
	}
	s3SessionToken, err := envOrFile("CONTINUITY_S3_SESSION_TOKEN", "CONTINUITY_S3_SESSION_TOKEN_FILE")
	if err != nil {
		return Config{}, err
	}
	jwtSecret, err := envOrFile("CONTINUITY_JWT_SECRET", "CONTINUITY_JWT_SECRET_FILE")
	if err != nil {
		return Config{}, err
	}
	bootstrapToken, err := envOrFile("CONTINUITY_BOOTSTRAP_TOKEN", "CONTINUITY_BOOTSTRAP_TOKEN_FILE")
	if err != nil {
		return Config{}, err
	}
	masterKey, err := envOrFile("CONTINUITY_MASTER_KEY", "CONTINUITY_MASTER_KEY_FILE")
	if err != nil {
		return Config{}, err
	}
	oidcJWKS, err := envOrFile("CONTINUITY_OIDC_JWKS", "CONTINUITY_OIDC_JWKS_FILE")
	if err != nil {
		return Config{}, err
	}
	scimBearerToken, err := envOrFile("CONTINUITY_SCIM_BEARER_TOKEN", "CONTINUITY_SCIM_BEARER_TOKEN_FILE")
	if err != nil {
		return Config{}, err
	}
	cfg := Config{WorkflowConfigJSON: workflowConfig, KMSConfigJSON: kmsConfig,
		ArchiveConfigJSON:    archiveConfig,
		PaymentConfigJSON:    paymentConfig,
		TreasuryConfigJSON:   treasuryConfig,
		BillingPriceBookJSON: priceBook,
		ListenAddress:        env("CONTINUITY_LISTEN_ADDRESS", ":8080"),
		Store:                env("CONTINUITY_STORE", "postgres"),
		DatabaseURL:          databaseURL,
		ArtifactDir:          env("CONTINUITY_ARTIFACT_DIR", ".data/artifacts"),
		ArtifactBackend:      env("CONTINUITY_ARTIFACT_BACKEND", "local"),
		S3Endpoint:           os.Getenv("CONTINUITY_S3_ENDPOINT"),
		S3Bucket:             os.Getenv("CONTINUITY_S3_BUCKET"),
		S3Region:             os.Getenv("CONTINUITY_S3_REGION"),
		S3AccessKey:          s3AccessKey,
		S3SecretKey:          s3SecretKey,
		S3SessionToken:       s3SessionToken,
		S3AutoCreate:         envBool("CONTINUITY_S3_AUTO_CREATE", false),
		JWTSecret:            jwtSecret,
		BootstrapToken:       bootstrapToken,
		MasterKey:            masterKey,
		OTLPEndpoint:         os.Getenv("CONTINUITY_OTLP_TRACES_ENDPOINT"),
		ServiceName:          env("CONTINUITY_SERVICE_NAME", "agent-continuity"),
		DataResidency:        strings.ToLower(strings.TrimSpace(os.Getenv("CONTINUITY_DATA_RESIDENCY"))),
		OIDCIssuer:           strings.TrimSpace(os.Getenv("CONTINUITY_OIDC_ISSUER")),
		OIDCAudience:         strings.TrimSpace(os.Getenv("CONTINUITY_OIDC_AUDIENCE")),
		OIDCJWKS:             oidcJWKS,
		OIDCOrganizationID:   strings.TrimSpace(os.Getenv("CONTINUITY_OIDC_ORGANIZATION_ID")),
		OIDCAutoProvision:    envBool("CONTINUITY_OIDC_AUTO_PROVISION", false),
		SCIMBearerToken:      scimBearerToken,
		SCIMOrganizationID:   strings.TrimSpace(os.Getenv("CONTINUITY_SCIM_ORGANIZATION_ID")),
		AutoMigrate:          envBool("CONTINUITY_AUTO_MIGRATE", false),
		ShutdownGrace:        envDuration("CONTINUITY_SHUTDOWN_GRACE", 15*time.Second),
		MaxBodyBytes:         envInt64("CONTINUITY_MAX_BODY_BYTES", 32<<20),
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func envOrFile(name, fileName string) (string, error) {
	value, path := os.Getenv(name), os.Getenv(fileName)
	if value != "" && path != "" {
		return "", fmt.Errorf("%s and %s are mutually exclusive", name, fileName)
	}
	if path == "" {
		return value, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", fileName, err)
	}
	value = strings.TrimRight(string(data), "\r\n")
	if value == "" {
		return "", fmt.Errorf("%s is empty", fileName)
	}
	return value, nil
}

func (c Config) Validate() error {
	if c.ListenAddress == "" {
		return errors.New("CONTINUITY_LISTEN_ADDRESS is required")
	}
	if strings.TrimSpace(c.ServiceName) == "" {
		return errors.New("CONTINUITY_SERVICE_NAME cannot be empty")
	}
	if c.Store != "postgres" && c.Store != "memory" {
		return errors.New("CONTINUITY_STORE must be postgres or memory")
	}
	if c.ArtifactBackend != "local" && c.ArtifactBackend != "s3" {
		return errors.New("CONTINUITY_ARTIFACT_BACKEND must be local or s3")
	}
	if c.ArtifactBackend == "s3" && (c.S3Endpoint == "" || c.S3Bucket == "" || c.S3AccessKey == "" || c.S3SecretKey == "") {
		return errors.New("CONTINUITY_S3_ENDPOINT, CONTINUITY_S3_BUCKET, CONTINUITY_S3_ACCESS_KEY, and CONTINUITY_S3_SECRET_KEY are required for S3 artifacts")
	}
	if c.Store == "postgres" && c.DatabaseURL == "" {
		return errors.New("CONTINUITY_DATABASE_URL is required for postgres store")
	}
	if len(c.JWTSecret) < 32 {
		return errors.New("CONTINUITY_JWT_SECRET must be at least 32 characters")
	}
	if len(c.BootstrapToken) < 24 {
		return errors.New("CONTINUITY_BOOTSTRAP_TOKEN must be at least 24 characters")
	}
	if _, err := secrets.DecodeKey(c.MasterKey); err != nil {
		return fmt.Errorf("CONTINUITY_MASTER_KEY: %w", err)
	}
	if c.MaxBodyBytes < 1<<20 || c.MaxBodyBytes > 1<<30 {
		return errors.New("CONTINUITY_MAX_BODY_BYTES must be between 1 MiB and 1 GiB")
	}
	oidcConfigured := c.OIDCIssuer != "" || c.OIDCAudience != "" || c.OIDCJWKS != ""
	if oidcConfigured && (c.OIDCIssuer == "" || c.OIDCAudience == "" || c.OIDCJWKS == "") {
		return errors.New("CONTINUITY_OIDC_ISSUER, CONTINUITY_OIDC_AUDIENCE, and OIDC JWKS are required together")
	}
	scimConfigured := c.SCIMBearerToken != "" || c.SCIMOrganizationID != ""
	if scimConfigured && (len(c.SCIMBearerToken) < 32 || c.SCIMOrganizationID == "") {
		return errors.New("SCIM requires a 32+ character bearer token and CONTINUITY_SCIM_ORGANIZATION_ID")
	}
	return nil
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func envBool(name string, fallback bool) bool {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envDuration(name string, fallback time.Duration) time.Duration {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envInt64(name string, fallback int64) int64 {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

func RedactedDatabaseURL(raw string) string {
	if at := strings.LastIndex(raw, "@"); at >= 0 {
		if scheme := strings.Index(raw, "://"); scheme >= 0 && scheme < at {
			return fmt.Sprintf("%s://***%s", raw[:scheme], raw[at:])
		}
	}
	return raw
}
