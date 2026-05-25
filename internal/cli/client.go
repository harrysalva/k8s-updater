package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"upgrade-guardian/internal/checker"
	"upgrade-guardian/internal/diff"
	"upgrade-guardian/internal/versions"
)

// Client wraps HTTP calls to the upgrade-guardian backend.
type Client struct {
	baseURL string
	hc      *http.Client
}

// NewClient creates a Client pointing to the upgrade-guardian backend.
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		hc:      &http.Client{Timeout: 120 * 1000000000}, // 2 min
	}
}

// GetCluster returns cluster info.
func (c *Client) GetCluster(context string) (map[string]string, error) {
	q := url.Values{}
	if context != "" {
		q.Set("context", context)
	}
	uri := c.baseURL + "/api/v1/cluster"
	if q.Encode() != "" {
		uri += "?" + q.Encode()
	}

	resp, err := c.hc.Get(uri)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, body)
	}

	var info map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return info, nil
}

// RunChecks executes the upgrade readiness checks.
func (c *Client) RunChecks(from, to, context string) (*checker.Report, error) {
	q := url.Values{
		"from": {from},
		"to":   {to},
	}
	if context != "" {
		q.Set("context", context)
	}
	uri := c.baseURL + "/api/v1/check?" + q.Encode()

	resp, err := c.hc.Get(uri)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, body)
	}

	var report checker.Report
	if err := json.NewDecoder(resp.Body).Decode(&report); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &report, nil
}

// GetVersions returns tool database coverage.
func (c *Client) GetVersions(target string) (*versions.Report, error) {
	q := url.Values{}
	if target != "" {
		q.Set("target", target)
	}
	uri := c.baseURL + "/api/v1/versions"
	if q.Encode() != "" {
		uri += "?" + q.Encode()
	}

	resp, err := c.hc.Get(uri)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, body)
	}

	var vr versions.Report
	if err := json.NewDecoder(resp.Body).Decode(&vr); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &vr, nil
}

// PostCheck compares a pre-upgrade report with a fresh check run.
func (c *Client) PostCheck(preReport *checker.Report, from, to, context string) (*diff.Result, error) {
	body := map[string]interface{}{
		"pre_report": preReport,
		"from":       from,
		"to":         to,
	}
	if context != "" {
		body["context"] = context
	}

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	resp, err := c.hc.Post(
		c.baseURL+"/api/v1/postcheck",
		"application/json",
		bytes.NewReader(bodyBytes),
	)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, body)
	}

	var result diff.Result
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &result, nil
}
