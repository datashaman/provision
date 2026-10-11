// Command valkey-http-fixture is a framework-neutral example workload for the
// key-value-bound HTTP acceptance. It binds to its Key-Value Store only by the
// logical references and encrypted credential file Provision delivers, and its
// candidate verification writes and reads back deterministic keys.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type key struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type binding struct {
	LogicalID    string `json:"logicalId"`
	GenerationID string `json:"generationId"`
	Status       string `json:"status"`
	Keys         []key  `json:"keys,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

func main() {
	listen := getenv("PROVISION_HTTP_LISTEN", "127.0.0.1:0")
	revision := getenv("PROVISION_REVISION", "unknown")
	logicalID, generation := os.Getenv("PROVISION_KEYVALUE_LOGICAL_ID"), os.Getenv("PROVISION_KEYVALUE_GENERATION")
	http.HandleFunc("/live", func(w http.ResponseWriter, _ *http.Request) {
		write(w, 200, map[string]string{"status": "live", "revision": revision})
	})
	http.HandleFunc("/ready", func(w http.ResponseWriter, _ *http.Request) {
		write(w, 200, map[string]string{"status": "ready", "revision": revision})
	})
	http.HandleFunc("/verify", func(w http.ResponseWriter, _ *http.Request) {
		keys, err := verify()
		if err != nil {
			write(w, 503, map[string]any{"revision": revision, "keyValueBinding": binding{LogicalID: logicalID, GenerationID: generation, Status: "failed", Reason: err.Error()}})
			return
		}
		write(w, 200, map[string]any{"revision": revision, "keyValueBinding": binding{LogicalID: logicalID, GenerationID: generation, Status: "verified", Keys: keys}})
	})
	log.Fatal(http.ListenAndServe(listen, nil))
}

func verify() ([]key, error) {
	data, err := os.ReadFile(os.Getenv("PROVISION_KEYVALUE_URL_FILE"))
	if err != nil {
		return nil, fmt.Errorf("read key-value credential file: %w", err)
	}
	u, err := url.Parse(strings.TrimSpace(string(data)))
	if err != nil || u.User == nil {
		return nil, errors.New("key-value credential is not a valkey URL")
	}
	password, _ := u.User.Password()
	conn, err := net.DialTimeout("tcp", u.Host, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("connect to key-value store: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	r := bufio.NewReader(conn)
	if reply, err := command(conn, r, "AUTH", u.User.Username(), password); err != nil || reply != "OK" {
		return nil, errors.New("key-value store rejected the workload credential")
	}
	prefix := os.Getenv("PROVISION_KEYVALUE_VALUE_PREFIX")
	var keys []key
	for _, k := range strings.Split(os.Getenv("PROVISION_KEYVALUE_KEYS"), ",") {
		if reply, err := command(conn, r, "SET", k, prefix+k); err != nil || reply != "OK" {
			return nil, errors.New("write deterministic key")
		}
		value, err := command(conn, r, "GET", k)
		if err != nil || value != prefix+k {
			return nil, errors.New("read back deterministic key")
		}
		keys = append(keys, key{Key: k, Value: value})
	}
	return keys, nil
}

// command sends one RESP command and returns a simple or bulk string reply.
func command(conn net.Conn, r *bufio.Reader, args ...string) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "*%d\r\n", len(args))
	for _, a := range args {
		fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(a), a)
	}
	if _, err := conn.Write([]byte(b.String())); err != nil {
		return "", err
	}
	line, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	line = strings.TrimRight(line, "\r\n")
	switch {
	case strings.HasPrefix(line, "+"):
		return line[1:], nil
	case strings.HasPrefix(line, "$"):
		n, err := strconv.Atoi(line[1:])
		if err != nil || n < 0 {
			return "", errors.New("empty bulk reply")
		}
		buf := make([]byte, n+2)
		if _, err := ioReadFull(r, buf); err != nil {
			return "", err
		}
		return string(buf[:n]), nil
	}
	return "", errors.New("unexpected reply")
}

func ioReadFull(r *bufio.Reader, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := r.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

func getenv(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}

func write(w http.ResponseWriter, status int, v any) {
	body, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
