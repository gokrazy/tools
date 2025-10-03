package packer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// TODO: move getting the remote build timestamp into the updater package
func pollUpdated1(ctx context.Context, updateHttpClient *http.Client, updateBaseUrl, targetBuildTimestamp string) error {
	// Cap each individual poll request to 5 seconds.
	ctx, canc := context.WithTimeout(ctx, 5*time.Second)
	defer canc()
	req, err := http.NewRequest("GET", updateBaseUrl, nil)
	if err != nil {
		return err
	}
	req = req.WithContext(ctx)
	req.Header.Set("Accept", "application/json")
	resp, err := updateHttpClient.Do(req)
	if err != nil {
		return err
	}
	if got, want := resp.StatusCode, http.StatusOK; got != want {
		return fmt.Errorf("unexpected HTTP status code: got %d, want %d", got, want)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	var status struct {
		BuildTimestamp string `json:"BuildTimestamp"`
	}
	if err := json.Unmarshal(b, &status); err != nil {
		return err
	}
	if got, want := status.BuildTimestamp, targetBuildTimestamp; got != want {
		return fmt.Errorf("device on old revision (%s), want %s", got, want)
	}
	return nil
}
