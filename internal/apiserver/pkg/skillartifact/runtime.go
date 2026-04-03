package skillartifact

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

const (
	defaultObjectKeyPattern = "skills/{skill_id}/{version}/bundle.zip"
	defaultPresignTTL       = 15 * time.Minute
	defaultManifestMaxBytes = 1 << 20
	defaultInstructionFile  = "SKILL.md"
	bundleFormatClaudeSkill = "claude_skill_v1"
)

var (
	ErrStorageDisabled    = errors.New("skill artifact storage is disabled")
	ErrUploadModeDisabled = errors.New("skill artifact upload mode is disabled")
	ErrInvalidArtifactRef = errors.New("invalid skill artifact ref")
)

type RuntimeConfig struct {
	Endpoint         string        `json:"endpoint" mapstructure:"endpoint"`
	Bucket           string        `json:"bucket" mapstructure:"bucket"`
	AccessKey        string        `json:"access_key" mapstructure:"access_key"`
	SecretKey        string        `json:"secret_key" mapstructure:"secret_key"`
	Region           string        `json:"region" mapstructure:"region"`
	PathStyle        bool          `json:"path_style" mapstructure:"path_style"`
	TLS              bool          `json:"tls" mapstructure:"tls"`
	SkipTLSVerify    bool          `json:"skip_tls_verify" mapstructure:"skip_tls_verify"`
	PrivateBucket    bool          `json:"private_bucket" mapstructure:"private_bucket"`
	ObjectKeyPattern string        `json:"object_key_pattern" mapstructure:"object_key_pattern"`
	UploadMode       string        `json:"upload_mode" mapstructure:"upload_mode"`
	DownloadMode     string        `json:"download_mode" mapstructure:"download_mode"`
	PresignTTL       time.Duration `json:"presign_ttl" mapstructure:"presign_ttl"`
}

type manager interface {
	Enabled() bool
	UploadBundle(ctx context.Context, skillID string, version string, raw []byte) (string, string, error)
	ResolveDownloadURL(ctx context.Context, artifactRef string) (string, error)
}

type SkillSummaryEnvelope struct {
	BundleFormat    string `json:"bundle_format"`
	InstructionFile string `json:"instruction_file"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	Compatibility   string `json:"compatibility,omitempty"`
}

type disabledManager struct{}

func (disabledManager) Enabled() bool {
	return false
}

func (disabledManager) UploadBundle(context.Context, string, string, []byte) (string, string, error) {
	return "", "", ErrStorageDisabled
}

func (disabledManager) ResolveDownloadURL(_ context.Context, artifactRef string) (string, error) {
	normalized := strings.TrimSpace(artifactRef)
	switch {
	case normalized == "":
		return "", ErrInvalidArtifactRef
	case hasDirectArtifactURL(normalized):
		return normalized, nil
	default:
		return "", ErrStorageDisabled
	}
}

type s3Manager struct {
	cfg         RuntimeConfig
	endpointURL *url.URL
	client      *minio.Client
}

var (
	runtimeMu      sync.RWMutex
	runtimeConfig          = DefaultRuntimeConfig()
	runtimeManager manager = disabledManager{}
)

func DefaultRuntimeConfig() RuntimeConfig {
	return RuntimeConfig{
		Region:           "us-east-1",
		PathStyle:        true,
		ObjectKeyPattern: defaultObjectKeyPattern,
		UploadMode:       "manual_register",
		DownloadMode:     "direct_url",
		PresignTTL:       defaultPresignTTL,
	}
}

func SetRuntimeConfig(cfg RuntimeConfig) error {
	normalized, mgr, err := buildManager(cfg)
	if err != nil {
		return err
	}
	runtimeMu.Lock()
	defer runtimeMu.Unlock()
	runtimeConfig = normalized
	runtimeManager = mgr
	return nil
}

func CurrentRuntimeConfig() RuntimeConfig {
	runtimeMu.RLock()
	defer runtimeMu.RUnlock()
	return runtimeConfig
}

func SetRuntimeManagerForTest(mgr manager) func() {
	runtimeMu.Lock()
	prevCfg := runtimeConfig
	prevMgr := runtimeManager
	if mgr == nil {
		runtimeManager = disabledManager{}
		runtimeConfig = DefaultRuntimeConfig()
	} else {
		runtimeManager = mgr
	}
	runtimeMu.Unlock()
	return func() {
		runtimeMu.Lock()
		runtimeConfig = prevCfg
		runtimeManager = prevMgr
		runtimeMu.Unlock()
	}
}

func UploadBundle(ctx context.Context, skillID string, version string, raw []byte) (string, string, error) {
	return currentManager().UploadBundle(ctx, skillID, version, raw)
}

func ResolveDownloadURL(ctx context.Context, artifactRef string) (string, error) {
	return currentManager().ResolveDownloadURL(ctx, artifactRef)
}

func ReadBundleSkillSummaryJSON(raw []byte) (string, error) {
	summary, err := ReadBundleSkillSummary(raw)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(summary)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(payload)), nil
}

func ReadBundleSkillSummary(raw []byte) (*SkillSummaryEnvelope, error) {
	if len(raw) == 0 {
		return nil, ErrInvalidArtifactRef
	}
	readerAt := bytes.NewReader(raw)
	archive, err := zip.NewReader(readerAt, int64(len(raw)))
	if err != nil {
		return nil, err
	}
	for _, file := range archive.File {
		if path.Clean(file.Name) != defaultInstructionFile {
			continue
		}
		handle, openErr := file.Open()
		if openErr != nil {
			return nil, openErr
		}
		defer handle.Close()
		limited := io.LimitReader(handle, defaultManifestMaxBytes+1)
		payload, readErr := io.ReadAll(limited)
		if readErr != nil {
			return nil, readErr
		}
		if int64(len(payload)) > defaultManifestMaxBytes {
			return nil, fmt.Errorf("skill file exceeds max bytes: %d", defaultManifestMaxBytes)
		}
		return parseSkillSummaryMarkdown(string(payload))
	}
	return nil, fmt.Errorf("skill bundle missing %s", defaultInstructionFile)
}

func ValidateSkillSummaryEnvelopeJSON(raw string) (*SkillSummaryEnvelope, error) {
	var payload SkillSummaryEnvelope
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, err
	}
	payload.BundleFormat = strings.TrimSpace(payload.BundleFormat)
	payload.InstructionFile = strings.TrimSpace(payload.InstructionFile)
	payload.Name = strings.TrimSpace(payload.Name)
	payload.Description = strings.TrimSpace(payload.Description)
	payload.Compatibility = strings.TrimSpace(payload.Compatibility)
	if payload.BundleFormat != bundleFormatClaudeSkill {
		return nil, fmt.Errorf("invalid bundle_format: %s", payload.BundleFormat)
	}
	if payload.InstructionFile != defaultInstructionFile {
		return nil, fmt.Errorf("invalid instruction_file: %s", payload.InstructionFile)
	}
	if payload.Name == "" || payload.Description == "" {
		return nil, fmt.Errorf("skill summary requires name and description")
	}
	return &payload, nil
}

func parseSkillSummaryMarkdown(raw string) (*SkillSummaryEnvelope, error) {
	content := strings.ReplaceAll(raw, "\r\n", "\n")
	if !strings.HasPrefix(content, "---\n") {
		return nil, fmt.Errorf("SKILL.md missing frontmatter")
	}
	rest := strings.TrimPrefix(content, "---\n")
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		return nil, fmt.Errorf("SKILL.md missing closing frontmatter delimiter")
	}
	frontmatter := rest[:end]
	fields := map[string]string{}
	for _, line := range strings.Split(frontmatter, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			return nil, fmt.Errorf("SKILL.md frontmatter only supports flat scalar fields")
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("invalid SKILL.md frontmatter line: %s", trimmed)
		}
		normalizedKey := strings.TrimSpace(key)
		normalizedValue := strings.TrimSpace(value)
		if normalizedKey == "" {
			return nil, fmt.Errorf("invalid SKILL.md frontmatter key")
		}
		if strings.Contains(normalizedValue, "\n") {
			return nil, fmt.Errorf("SKILL.md frontmatter multiline values are unsupported")
		}
		if len(normalizedValue) >= 2 {
			if (strings.HasPrefix(normalizedValue, "\"") && strings.HasSuffix(normalizedValue, "\"")) ||
				(strings.HasPrefix(normalizedValue, "'") && strings.HasSuffix(normalizedValue, "'")) {
				normalizedValue = normalizedValue[1 : len(normalizedValue)-1]
			}
		}
		fields[normalizedKey] = normalizedValue
	}

	summary := &SkillSummaryEnvelope{
		BundleFormat:    bundleFormatClaudeSkill,
		InstructionFile: defaultInstructionFile,
		Name:            strings.TrimSpace(fields["name"]),
		Description:     strings.TrimSpace(fields["description"]),
		Compatibility:   strings.TrimSpace(fields["compatibility"]),
	}
	if summary.Name == "" || summary.Description == "" {
		return nil, fmt.Errorf("SKILL.md frontmatter requires name and description")
	}
	return summary, nil
}

func currentManager() manager {
	runtimeMu.RLock()
	defer runtimeMu.RUnlock()
	return runtimeManager
}

func buildManager(cfg RuntimeConfig) (RuntimeConfig, manager, error) {
	normalized := normalizeConfig(cfg)
	if !hasStorageSettings(normalized) {
		return normalized, disabledManager{}, nil
	}
	if normalized.Endpoint == "" || normalized.Bucket == "" || normalized.AccessKey == "" || normalized.SecretKey == "" {
		return normalized, nil, fmt.Errorf("skill artifact storage requires endpoint, bucket, access_key, secret_key")
	}
	parsed, err := parseEndpoint(normalized.Endpoint, normalized.TLS)
	if err != nil {
		return normalized, nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if normalized.SkipTLSVerify {
		if transport.TLSClientConfig == nil {
			transport.TLSClientConfig = &tls.Config{}
		}
		transport.TLSClientConfig.InsecureSkipVerify = true
	}
	bucketLookup := minio.BucketLookupAuto
	if normalized.PathStyle {
		bucketLookup = minio.BucketLookupPath
	}
	client, err := minio.New(parsed.Host, &minio.Options{
		Creds:        credentials.NewStaticV4(normalized.AccessKey, normalized.SecretKey, ""),
		Secure:       parsed.Scheme == "https",
		Transport:    transport,
		Region:       normalized.Region,
		BucketLookup: bucketLookup,
	})
	if err != nil {
		return normalized, nil, err
	}
	return normalized, &s3Manager{
		cfg:         normalized,
		endpointURL: parsed,
		client:      client,
	}, nil
}

func normalizeConfig(cfg RuntimeConfig) RuntimeConfig {
	out := DefaultRuntimeConfig()
	if strings.TrimSpace(cfg.Endpoint) != "" {
		out.Endpoint = strings.TrimSpace(cfg.Endpoint)
	}
	out.Bucket = strings.TrimSpace(cfg.Bucket)
	out.AccessKey = strings.TrimSpace(cfg.AccessKey)
	out.SecretKey = strings.TrimSpace(cfg.SecretKey)
	if strings.TrimSpace(cfg.Region) != "" {
		out.Region = strings.TrimSpace(cfg.Region)
	}
	out.PathStyle = cfg.PathStyle
	out.TLS = cfg.TLS
	out.SkipTLSVerify = cfg.SkipTLSVerify
	out.PrivateBucket = cfg.PrivateBucket
	if strings.TrimSpace(cfg.ObjectKeyPattern) != "" {
		out.ObjectKeyPattern = strings.TrimSpace(cfg.ObjectKeyPattern)
	}
	if strings.TrimSpace(cfg.UploadMode) != "" {
		out.UploadMode = strings.ToLower(strings.TrimSpace(cfg.UploadMode))
	}
	if strings.TrimSpace(cfg.DownloadMode) != "" {
		out.DownloadMode = strings.ToLower(strings.TrimSpace(cfg.DownloadMode))
	}
	if cfg.PresignTTL > 0 {
		out.PresignTTL = cfg.PresignTTL
	}
	return out
}

func hasStorageSettings(cfg RuntimeConfig) bool {
	return cfg.Endpoint != "" || cfg.Bucket != "" || cfg.AccessKey != "" || cfg.SecretKey != ""
}

func parseEndpoint(raw string, useTLS bool) (*url.URL, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return nil, ErrStorageDisabled
	}
	if !strings.Contains(value, "://") {
		scheme := "http"
		if useTLS {
			scheme = "https"
		}
		value = scheme + "://" + value
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return nil, err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("unsupported skill artifact endpoint scheme: %s", parsed.Scheme)
	}
	if strings.TrimSpace(parsed.Host) == "" {
		return nil, fmt.Errorf("skill artifact endpoint host is required")
	}
	return parsed, nil
}

func (m *s3Manager) Enabled() bool {
	return m != nil && m.client != nil
}

func (m *s3Manager) UploadBundle(ctx context.Context, skillID string, version string, raw []byte) (string, string, error) {
	if m == nil || m.client == nil {
		return "", "", ErrStorageDisabled
	}
	if m.cfg.UploadMode != "api_upload" {
		return "", "", ErrUploadModeDisabled
	}
	objectKey, err := m.objectKey(skillID, version)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	_, err = m.client.PutObject(
		ctx,
		m.cfg.Bucket,
		objectKey,
		bytes.NewReader(raw),
		int64(len(raw)),
		minio.PutObjectOptions{ContentType: "application/zip"},
	)
	if err != nil {
		return "", "", err
	}
	return fmt.Sprintf("s3://%s/%s", m.cfg.Bucket, objectKey), digest, nil
}

func (m *s3Manager) ResolveDownloadURL(ctx context.Context, artifactRef string) (string, error) {
	normalized := strings.TrimSpace(artifactRef)
	if normalized == "" {
		return "", ErrInvalidArtifactRef
	}
	if hasDirectArtifactURL(normalized) {
		return normalized, nil
	}
	bucketName, objectKey, err := parseArtifactRef(normalized)
	if err != nil {
		return "", err
	}
	if m.cfg.DownloadMode == "presigned_url" || m.cfg.PrivateBucket {
		u, presignErr := m.client.PresignedGetObject(ctx, bucketName, objectKey, m.cfg.PresignTTL, nil)
		if presignErr != nil {
			return "", presignErr
		}
		return u.String(), nil
	}
	return m.directObjectURL(bucketName, objectKey).String(), nil
}

func (m *s3Manager) objectKey(skillID string, version string) (string, error) {
	normalizedSkillID := strings.TrimSpace(skillID)
	normalizedVersion := strings.TrimSpace(version)
	if normalizedSkillID == "" || normalizedVersion == "" {
		return "", ErrInvalidArtifactRef
	}
	if strings.ContainsAny(normalizedSkillID, `/\`) || strings.ContainsAny(normalizedVersion, `/\`) {
		return "", ErrInvalidArtifactRef
	}
	key := strings.NewReplacer(
		"{skill_id}", normalizedSkillID,
		"{version}", normalizedVersion,
	).Replace(m.cfg.ObjectKeyPattern)
	key = strings.TrimSpace(strings.TrimPrefix(key, "/"))
	if key == "" || strings.Contains(key, "{") || strings.Contains(key, "}") {
		return "", ErrInvalidArtifactRef
	}
	if strings.Contains(key, "..") {
		return "", ErrInvalidArtifactRef
	}
	return key, nil
}

func (m *s3Manager) directObjectURL(bucketName string, objectKey string) *url.URL {
	cloned := *m.endpointURL
	if m.cfg.PathStyle {
		cloned.Path = path.Join("/", bucketName, objectKey)
		return &cloned
	}
	cloned.Host = bucketName + "." + cloned.Host
	cloned.Path = path.Join("/", objectKey)
	return &cloned
}

func parseArtifactRef(ref string) (string, string, error) {
	parsed, err := url.Parse(strings.TrimSpace(ref))
	if err != nil {
		return "", "", err
	}
	if parsed.Scheme != "s3" {
		return "", "", ErrInvalidArtifactRef
	}
	bucketName := strings.TrimSpace(parsed.Host)
	objectKey := strings.TrimSpace(strings.TrimPrefix(parsed.Path, "/"))
	if bucketName == "" || objectKey == "" {
		return "", "", ErrInvalidArtifactRef
	}
	return bucketName, objectKey, nil
}

func hasDirectArtifactURL(ref string) bool {
	return strings.HasPrefix(ref, "http://") ||
		strings.HasPrefix(ref, "https://") ||
		strings.HasPrefix(ref, "file://")
}
