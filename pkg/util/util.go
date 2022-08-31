package util

import (
	"net/http"
	"time"
)

func WithRetry(fn func() error) error {
	var finalErr error
	for i := 0; i < 3; i += 1 {
		err := fn()
		if err == nil {
			return nil
		}
		finalErr = err
		time.Sleep(5 * time.Second)
	}
	return finalErr
}

func WaitUntilReady(ipAddr string) error {
	var err error
	for retry := 0; retry < 3; retry += 1 {
		resp, err := http.Get("http://" + ipAddr + ":8091/ui/index.html")
		if err == nil && resp.StatusCode == 200 {
			return nil
		}
		time.Sleep(5 * time.Second)
	}
	return err
}
