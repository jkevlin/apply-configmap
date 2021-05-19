package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/hashicorp/go-cleanhttp"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-retryablehttp"
	"gopkg.in/yaml.v2"
)

var (
	// Retry configuration
	RetryWaitMin = 500 * time.Millisecond
	RetryWaitMax = 30 * time.Second
	RetryMax     = 10

	// Standard errs
	ErrNamespaceUnset     = errors.New(`"namespace" is unset`)
	ErrConfigMapNameUnset = errors.New(`"configmapName" is unset`)
	ErrNotInCluster       = errors.New("unable to load in-cluster configuration, KUBERNETES_SERVICE_HOST and KUBERNETES_SERVICE_PORT must be defined")
)

// Client is a minimal Kubernetes client. We rolled our own because the existing
// Kubernetes client-go library available externally has a high number of dependencies
// and we thought it wasn't worth it for only two API calls. If at some point they break
// the client into smaller modules, or if we add quite a few methods to this client, it may
// be worthwhile to revisit that decision.
type Client struct {
	logger hclog.Logger
	config *Config
	stopCh chan struct{}
}

// New instantiates a Client. The stopCh is used for exiting retry loops
// when closed.
func New(logger hclog.Logger) (*Client, error) {
	config, err := inClusterConfig()
	if err != nil {
		return nil, err
	}
	return &Client{
		logger: logger,
		config: config,
		stopCh: make(chan struct{}),
	}, nil
}

func (c *Client) Shutdown() {
	close(c.stopCh)
}

// GetConfigMap gets a configmap from the Kubernetes API.
func (c *Client) GetConfigMap(namespace, configmapName string) (*ConfigMap, error) {
	endpoint := fmt.Sprintf("/api/v1/namespaces/%s/configmaps/%s", namespace, configmapName)
	method := http.MethodGet

	// Validate that we received required parameters.
	if namespace == "" {
		return nil, ErrNamespaceUnset
	}
	if configmapName == "" {
		return nil, ErrConfigMapNameUnset
	}

	req, err := http.NewRequest(method, c.config.Host+endpoint, nil)
	if err != nil {
		return nil, err
	}
	configmap := &ConfigMap{}
	if err := c.do(req, configmap); err != nil {
		return nil, err
	}
	return configmap, nil
}

// ApplyConfigMap gets a configmap from the Kubernetes API.
func (c *Client) ApplyConfigMap(namespace, applyFilename string) error {
	yamlFile, err := ioutil.ReadFile(applyFilename)
	if err != nil {
		return fmt.Errorf("yamlFile.Get err   #%v ", err)
	}
	s := ConfigMap{}
	err = yaml.Unmarshal(yamlFile, &s)
	if err != nil {
		return fmt.Errorf("Unmarshal: %v", err)
	}
	if s.Metadata.Name == "" {
		return fmt.Errorf("YAML file must have configmap name")
	}
	configmapName := s.Metadata.Name

	method := http.MethodPost
	endpoint := fmt.Sprintf("/api/v1/namespaces/%s/configmaps", namespace)
	_, err = c.GetConfigMap(namespace, configmapName)
	if err == nil { // no error means it found it
		fmt.Println("updating configmap")
		method = http.MethodPut
		endpoint = fmt.Sprintf("/api/v1/namespaces/%s/configmaps/%s", namespace, configmapName)
	} else {
		fmt.Println("creating configmap")
	}

	// Validate that we received required parameters.
	if namespace == "" {
		return ErrNamespaceUnset
	}
	if configmapName == "" {
		return ErrConfigMapNameUnset
	}

	body, err := json.Marshal(s)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(method, c.config.Host+endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	configmap := &ConfigMap{}
	if err := c.do(req, configmap); err != nil {
		return err
	}
	return nil
}

// do executes the given request, retrying if necessary.
func (c *Client) do(req *http.Request, ptrToReturnObj interface{}) error {
	// Finish setting up a valid request.
	retryableReq, err := retryablehttp.FromRequest(req)
	if err != nil {
		return err
	}

	// Build a context that will call the cancelFunc when we receive
	// a stop from our stopChan. This allows us to exit from our retry
	// loop during a shutdown, rather than hanging.
	ctx, cancelFunc := context.WithCancel(context.Background())
	go func() {
		select {
		case <-ctx.Done():
		case <-c.stopCh:
			cancelFunc()
		}
	}()
	retryableReq.WithContext(ctx)

	retryableReq.Header.Set("Authorization", "Bearer "+c.config.BearerToken)
	retryableReq.Header.Set("Accept", "application/json")

	client := &retryablehttp.Client{
		HTTPClient:   cleanhttp.DefaultClient(),
		RetryWaitMin: RetryWaitMin,
		RetryWaitMax: RetryWaitMax,
		RetryMax:     RetryMax,
		CheckRetry:   c.getCheckRetry(req),
		Backoff:      retryablehttp.DefaultBackoff,
	}
	client.HTTPClient.Transport = &http.Transport{
		TLSClientConfig: &tls.Config{
			RootCAs: c.config.CACertPool,
		},
	}

	// Execute and retry the request. This client comes with exponential backoff and
	// jitter already rolled in.
	resp, err := client.Do(retryableReq)
	if err != nil {
		return err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			if c.logger.IsWarn() {
				// Failing to close response bodies can present as a memory leak so it's
				// important to surface it.
				c.logger.Warn(fmt.Sprintf("unable to close response body: %s", err))
			}
		}
	}()

	// If we're not supposed to read out the body, we have nothing further
	// to do here.
	if ptrToReturnObj == nil {
		return nil
	}

	// Attempt to read out the body into the given return object.
	return json.NewDecoder(resp.Body).Decode(ptrToReturnObj)
}

func (c *Client) getCheckRetry(req *http.Request) retryablehttp.CheckRetry {
	return func(ctx context.Context, resp *http.Response, err error) (bool, error) {
		if resp == nil {
			return true, fmt.Errorf("nil response: %s", req.URL.RequestURI())
		}
		switch resp.StatusCode {
		case 200, 201, 202, 204:
			// Success.
			return false, nil
		case 401, 403:
			// Perhaps the token from our bearer token file has been refreshed.
			config, err := inClusterConfig()
			if err != nil {
				return false, err
			}
			if config.BearerToken == c.config.BearerToken {
				// It's the same token.
				return false, fmt.Errorf("bad status code: %s", sanitizedDebuggingInfo(req, resp.StatusCode))
			}
			c.config = config
			// Continue to try again, but return the error too in case the caller would rather read it out.
			return true, fmt.Errorf("bad status code: %s", sanitizedDebuggingInfo(req, resp.StatusCode))
		case 404:
			return false, &ErrNotFound{debuggingInfo: sanitizedDebuggingInfo(req, resp.StatusCode)}
		case 500, 502, 503, 504:
			// Could be transient.
			return true, fmt.Errorf("unexpected status code: %s", sanitizedDebuggingInfo(req, resp.StatusCode))
		}
		// Unexpected.
		return false, fmt.Errorf("unexpected status code: %s", sanitizedDebuggingInfo(req, resp.StatusCode))
	}
}

type ConfigMap struct {
	ApiVersion string            `json:"apiVersion,omitempty" yaml:"apiVersion,omitempty"`
	Kind       string            `json:"kind,omitempty"`
	Metadata   *Metadata         `json:"metadata,omitempty"`
	Data       map[string]string `json:"data,omitempty"`
	BinaryData map[string][]byte `json:"binaryData,omitempty"`
}

type Metadata struct {
	Name string `json:"name,omitempty"`

	// This map will be nil if no "labels" key was provided.
	// It will be populated but have a length of zero if the
	// key was provided, but no values.
	Labels map[string]string `json:"labels,omitempty"`
}

type ErrNotFound struct {
	debuggingInfo string
}

func (e *ErrNotFound) Error() string {
	return e.debuggingInfo
}

// Sanitize is for "data" being sent to the Kubernetes API.
// Data must consist of alphanumeric characters, '-', '_' or '.'.
// Any other characters found in the original value will be stripped,
// and the surrounding characters will be concatenated.
func Sanitize(val string) string {
	return strings.Map(replaceBadCharsWithDashes, val)
}

func replaceBadCharsWithDashes(r rune) rune {
	if unicode.IsLetter(r) {
		return r
	}
	if unicode.IsNumber(r) {
		return r
	}
	switch string(r) {
	case "-", "_", ".":
		return r
	}
	return '-'
}

// sanitizedDebuggingInfo provides a returnable string that can be used for debugging. This is intentionally somewhat vague
// because we don't want to leak configmaps that may be in a request or response body.
func sanitizedDebuggingInfo(req *http.Request, respStatus int) string {
	return fmt.Sprintf("req method: %s, req url: %s, resp statuscode: %d", req.Method, req.URL, respStatus)
}
