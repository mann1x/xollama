package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/ollama/ollama/envconfig"
)

// keyedClient gives a client of the configured server its local API key:
// inside the server itself the server's own token, elsewhere XOLLAMA_API_KEY
// or the user's key file. Only ClientFromEnvironment calls it: a client made with
// NewClient for another host (a council member elsewhere, a remote model)
// never carries this server's key.
func keyedClient(c *Client) *Client {
	// Inside the server its own token comes first: a key file left in the
	// service account's home must not break the server's calls to itself.
	c.apiKey = processAPIKey()
	if c.apiKey == "" {
		c.apiKey = envconfig.ClientAPIKey()
	}
	if c.apiKey == "" {
		return c
	}
	// A redirect is followed only to the same host: net/http strips
	// Authorization across domains but forwards x-api-key, and a redirect is
	// exactly how a hostile or misconfigured endpoint would steer the key
	// elsewhere.
	base := c.base
	hc := *c.http
	hc.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Host != base.Host || req.URL.Scheme != base.Scheme {
			return errRedirectWithKey
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	c.http = &hc
	return c
}

var errRedirectWithKey = errors.New("refusing to follow a redirect to another host with the xOllama API key")

// setAPIKey puts the key on a request. Authorization carries it; when that
// header already carries an ollama.com signature (OLLAMA_AUTH), x-api-key
// does.
func (c *Client) setAPIKey(r *http.Request) {
	if c.apiKey == "" {
		return
	}
	if r.Header.Get("Authorization") == "" {
		r.Header.Set("Authorization", "Bearer "+c.apiKey)
		return
	}
	r.Header.Set("x-api-key", c.apiKey)
}

// HasAPIKey reports whether this client sends a local API key.
func (c *Client) HasAPIKey() bool { return c.apiKey != "" }

// localKeyError turns a 401 from a server's local API key into an error that
// says so. Without it the 401 would read as an ollama.com AuthorizationError
// and the CLI would offer an ollama.com sign-in, which cannot help.
func localKeyError(resp *http.Response, body []byte) error {
	if resp.StatusCode != http.StatusUnauthorized || !keyedChallenge(resp) {
		return nil
	}
	var e struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &e)
	msg := e.Error
	if msg == "" {
		msg = "this xOllama server requires an API key"
	}
	return StatusError{
		StatusCode:   resp.StatusCode,
		Status:       resp.Status,
		ErrorMessage: msg + " (set XOLLAMA_API_KEY, or save it in ~/.ollama/" + envconfig.ClientKeyFile + ")",
	}
}
