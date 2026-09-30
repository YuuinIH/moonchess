package state

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"
)

var ErrNotFound = errors.New("snapshot not found")

// Store is the Mooncake data-plane seam. A native client can replace the REST
// adapter without changing the game or worker packages.
type Store interface {
	Put(context.Context, string, []byte) error
	Get(context.Context, string) ([]byte, error)
	Remove(context.Context, string) error
}

// MooncakeHTTPStore uses the official Store REST API shipped with Mooncake.
type MooncakeHTTPStore struct {
	baseURL string
	client  *http.Client
}

func NewMooncakeHTTPStore(baseURL string, client *http.Client) *MooncakeHTTPStore {
	if client == nil {
		client = http.DefaultClient
	}
	return &MooncakeHTTPStore{baseURL: strings.TrimRight(baseURL, "/"), client: client}
}

func (s *MooncakeHTTPStore) Put(ctx context.Context, key string, value []byte) error {
	if !utf8.Valid(value) {
		return errors.New("Mooncake REST adapter accepts UTF-8 snapshots only")
	}
	body, err := json.Marshal(struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}{Key: key, Value: string(value)})
	if err != nil {
		return fmt.Errorf("encode put: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, s.baseURL+"/api/put", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create put request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("Mooncake put: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("Mooncake put returned %s: %s", resp.Status, strings.TrimSpace(string(message)))
	}
	return nil
}

func (s *MooncakeHTTPStore) Get(ctx context.Context, key string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+"/api/get/"+url.PathEscape(key), nil)
	if err != nil {
		return nil, fmt.Errorf("create get request: %w", err)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Mooncake get: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if resp.StatusCode/100 != 2 {
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("Mooncake get returned %s: %s", resp.Status, strings.TrimSpace(string(message)))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, fmt.Errorf("read Mooncake snapshot: %w", err)
	}
	return data, nil
}

func (s *MooncakeHTTPStore) Remove(ctx context.Context, key string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, s.baseURL+"/api/remove/"+url.PathEscape(key), nil)
	if err != nil {
		return err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("Mooncake remove returned %s", resp.Status)
	}
	return nil
}
