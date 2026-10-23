// Package sample is a fixture for the archmap API extraction test.
package sample

// Store keeps things. It is an interface.
type Store interface {
	Get(key string) (string, error)
	Put(key, value string) error
}

// Client talks to a Store.
type Client struct {
	Name  string
	store Store
}

// Mode picks how a Client behaves.
type Mode int

// The modes.
const (
	ModeFast Mode = iota
	ModeSafe
)

// MaxItems caps a batch.
const MaxItems = 100

// NewClient returns a Client over s.
func NewClient(name string, s Store) *Client { return &Client{Name: name, store: s} }

// Fetch reads one key.
func (c *Client) Fetch(key string) (string, error) { return c.store.Get(key) }

func (c *Client) helper() {}

// Version is the fixture version.
func Version() string { return "1" }

func unexported() {}
