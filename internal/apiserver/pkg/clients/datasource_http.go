package clients

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
)

// DatasourceHTTPClient issues read-only queries to configured datasources.
type DatasourceHTTPClient struct {
	httpClient *http.Client
}

// NewDatasourceHTTPClient creates a new datasource HTTP client.
func NewDatasourceHTTPClient() *DatasourceHTTPClient {
	return &DatasourceHTTPClient{
		httpClient: &http.Client{},
	}
}

// QueryPrometheusRange queries prometheus query_range API.
func (c *DatasourceHTTPClient) QueryPrometheusRange(
	ctx context.Context,
	ds *model.DatasourceM,
	promql string,
	start time.Time,
	end time.Time,
	stepSeconds int64,
) (map[string]any, int64, error) {
	values := url.Values{}
	values.Set("query", promql)
	values.Set("start", strconv.FormatInt(start.Unix(), 10))
	values.Set("end", strconv.FormatInt(end.Unix(), 10))
	values.Set("step", strconv.FormatInt(stepSeconds, 10))

	uri, err := withPathAndQuery(ds.BaseURL, "/api/v1/query_range", values)
	if err != nil {
		return nil, 0, err
	}

	raw, err := c.doJSONRequest(ctx, ds, http.MethodGet, uri, nil)
	if err != nil {
		return nil, 0, err
	}

	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, 0, fmt.Errorf("parse prometheus response: %w", err)
	}

	rowCount := int64(0)
	if data, ok := out["data"].(map[string]any); ok {
		if result, ok := data["result"].([]any); ok {
			rowCount = int64(len(result))
		}
	}

	return out, rowCount, nil
}

// QueryLokiRange queries loki query_range API.
func (c *DatasourceHTTPClient) QueryLokiRange(
	ctx context.Context,
	ds *model.DatasourceM,
	queryText string,
	start time.Time,
	end time.Time,
	limit int64,
) (map[string]any, int64, error) {
	values := url.Values{}
	values.Set("query", queryText)
	values.Set("start", strconv.FormatInt(start.UnixNano(), 10))
	values.Set("end", strconv.FormatInt(end.UnixNano(), 10))
	values.Set("limit", strconv.FormatInt(limit, 10))
	values.Set("direction", "backward")

	uri, err := withPathAndQuery(ds.BaseURL, "/loki/api/v1/query_range", values)
	if err != nil {
		return nil, 0, err
	}

	raw, err := c.doJSONRequest(ctx, ds, http.MethodGet, uri, nil)
	if err != nil {
		return nil, 0, err
	}

	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, 0, fmt.Errorf("parse loki response: %w", err)
	}

	rowCount := int64(0)
	if data, ok := out["data"].(map[string]any); ok {
		if result, ok := data["result"].([]any); ok {
			for _, item := range result {
				stream, ok := item.(map[string]any)
				if !ok {
					continue
				}
				values, ok := stream["values"].([]any)
				if !ok {
					continue
				}
				rowCount += int64(len(values))
			}
		}
	}

	return out, rowCount, nil
}

// QueryElasticsearch performs a best-effort read-only search on elasticsearch.
func (c *DatasourceHTTPClient) QueryElasticsearch(
	ctx context.Context,
	ds *model.DatasourceM,
	queryText string,
	queryJSON *string,
	start time.Time,
	end time.Time,
	limit int64,
) (map[string]any, int64, error) {
	var body []byte
	if queryJSON != nil && strings.TrimSpace(*queryJSON) != "" {
		body = []byte(*queryJSON)
	} else {
		payload := map[string]any{
			"query": map[string]any{
				"bool": map[string]any{
					"must": []any{
						map[string]any{
							"query_string": map[string]any{
								"query": queryText,
							},
						},
					},
					"filter": []any{
						map[string]any{
							"range": map[string]any{
								"@timestamp": map[string]any{
									"gte": start.UTC().Format(time.RFC3339Nano),
									"lte": end.UTC().Format(time.RFC3339Nano),
								},
							},
						},
					},
				},
			},
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, 0, fmt.Errorf("marshal elasticsearch query: %w", err)
		}
		body = raw
	}

	values := url.Values{}
	values.Set("size", strconv.FormatInt(limit, 10))
	values.Set("sort", "@timestamp:desc")
	uri, err := withPathAndQuery(ds.BaseURL, "/_search", values)
	if err != nil {
		return nil, 0, err
	}

	raw, err := c.doJSONRequest(ctx, ds, http.MethodPost, uri, body)
	if err != nil {
		return nil, 0, err
	}

	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, 0, fmt.Errorf("parse elasticsearch response: %w", err)
	}

	rowCount := int64(0)
	if hitsWrap, ok := out["hits"].(map[string]any); ok {
		if hits, ok := hitsWrap["hits"].([]any); ok {
			rowCount = int64(len(hits))
		}
	}

	return out, rowCount, nil
}

func (c *DatasourceHTTPClient) doJSONRequest(
	ctx context.Context,
	ds *model.DatasourceM,
	method string,
	uri string,
	body []byte,
) ([]byte, error) {
	var bodyReader io.Reader
	if len(body) > 0 {
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, uri, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}

	if ds.DefaultHeadersJSON != nil && strings.TrimSpace(*ds.DefaultHeadersJSON) != "" {
		headerMap := map[string]string{}
		if err := json.Unmarshal([]byte(*ds.DefaultHeadersJSON), &headerMap); err == nil {
			for key, value := range headerMap {
				req.Header.Set(key, value)
			}
		}
	}

	applyDatasourceAuth(req, ds)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= http.StatusBadRequest {
		if len(raw) > 512 {
			raw = raw[:512]
		}
		return nil, fmt.Errorf("upstream status=%d body=%s", resp.StatusCode, string(raw))
	}

	return raw, nil
}

func withPathAndQuery(baseURL string, path string, query url.Values) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return "", fmt.Errorf("invalid base_url: %w", err)
	}

	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("base_url must include scheme and host")
	}

	basePath := strings.TrimSuffix(parsed.Path, "/")
	fullPath := basePath + path
	if !strings.HasPrefix(fullPath, "/") {
		fullPath = "/" + fullPath
	}
	parsed.Path = fullPath
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func applyDatasourceAuth(req *http.Request, ds *model.DatasourceM) {
	secret := ""
	if ds.AuthSecretRef != nil {
		secret = strings.TrimSpace(*ds.AuthSecretRef)
	}

	switch strings.ToLower(strings.TrimSpace(ds.AuthType)) {
	case "basic":
		if secret == "" {
			return
		}
		user := ""
		pass := ""
		parts := strings.SplitN(secret, ":", 2)
		user = parts[0]
		if len(parts) == 2 {
			pass = parts[1]
		}
		req.SetBasicAuth(user, pass)
	case "bearer":
		if secret != "" {
			req.Header.Set("Authorization", "Bearer "+secret)
		}
	case "api_key":
		if secret != "" {
			req.Header.Set("X-API-Key", secret)
		}
	default:
		// none
	}

	// Optional compatibility: if caller put "Basic base64" in authSecretRef.
	if strings.HasPrefix(secret, "Basic ") {
		req.Header.Set("Authorization", secret)
		return
	}

	if strings.HasPrefix(secret, "b64:") {
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, "b64:"))
		if err == nil && len(decoded) > 0 {
			req.Header.Set("Authorization", string(decoded))
		}
	}
}
