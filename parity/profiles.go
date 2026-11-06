package parity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
)

// Profile — нэг auth профайл. Дараахын НЭГИЙГ тохируулна:
//
//	token / token_env / token_file — статик bearer token
//	login_file                      — {"login":..,"password":..} агуулсан файлын зам;
//	                                  runtime-д уншиж POST login_path (default /auth/login)
//
// Нууц үгийг хэзээ ч profiles.json-д шууд бичихгүй — зөвхөн файлын зам.
type Profile struct {
	Token     string            `json:"token,omitempty"`
	TokenEnv  string            `json:"token_env,omitempty"`
	TokenFile string            `json:"token_file,omitempty"`
	LoginFile string            `json:"login_file,omitempty"`
	LoginPath string            `json:"login_path,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"` // нэмэлт header (жишээ нь X-Org-ID)
}

// Profiles — profiles.json: {"profiles": {"admin": {...}}} эсвэл шууд {"admin": {...}}.
type Profiles map[string]Profile

// LoadProfiles — profiles.json унших.
func LoadProfiles(path string) (Profiles, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var wrap struct {
		Profiles Profiles `json:"profiles"`
	}
	if err := json.Unmarshal(b, &wrap); err == nil && len(wrap.Profiles) > 0 {
		return wrap.Profiles, nil
	}
	var p Profiles
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("profiles: %w", err)
	}
	return p, nil
}

// Credentials — login_file-ийн бүтэц.
type Credentials struct {
	Login    string `json:"login"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// authState — нэг талын (A эсвэл B) профайл бүрийн token-ыг нэг удаа авч кэшлэнэ.
type authState struct {
	base     string
	client   *http.Client
	profiles Profiles
	red      *Redactor
	mu       sync.Mutex
	cache    map[string]resolved
}

type resolved struct {
	token   string
	headers map[string]string
}

func newAuthState(base string, c *http.Client, p Profiles, red *Redactor) *authState {
	return &authState{base: strings.TrimRight(base, "/"), client: c, profiles: p, red: red, cache: map[string]resolved{}}
}

// get — профайлын header-үүдийг (Authorization-тэй) буцаана. name хоосон бол nil.
func (a *authState) get(ctx context.Context, name string) (map[string]string, error) {
	if name == "" {
		return nil, nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if r, ok := a.cache[name]; ok {
		return r.headers, nil
	}
	p, ok := a.profiles[name]
	if !ok {
		return nil, fmt.Errorf("auth profile %q олдсонгүй", name)
	}
	tok, err := a.token(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("auth profile %q: %w", name, err)
	}
	a.red.Add(tok)
	h := map[string]string{}
	for k, v := range p.Headers {
		h[k] = v
	}
	if tok != "" {
		h["Authorization"] = "Bearer " + tok
	}
	a.cache[name] = resolved{tok, h}
	return h, nil
}

func (a *authState) token(ctx context.Context, p Profile) (string, error) {
	switch {
	case p.Token != "":
		return p.Token, nil
	case p.TokenEnv != "":
		return os.Getenv(p.TokenEnv), nil
	case p.TokenFile != "":
		b, err := os.ReadFile(p.TokenFile)
		return strings.TrimSpace(string(b)), err
	case p.LoginFile != "":
		return a.login(ctx, p)
	}
	return "", nil // зөвхөн header-тэй профайл
}

func (a *authState) login(ctx context.Context, p Profile) (string, error) {
	b, err := os.ReadFile(p.LoginFile) // runtime-д унших; агуулгыг хэзээ ч лог хийхгүй
	if err != nil {
		return "", fmt.Errorf("login_file уншиж чадсангүй: %w", err)
	}
	var c Credentials
	if err := json.Unmarshal(b, &c); err != nil {
		return "", fmt.Errorf("login_file JSON биш")
	}
	if c.Login == "" {
		c.Login = c.Username
	}
	a.red.Add(c.Password)
	body, _ := json.Marshal(map[string]string{"login": c.Login, "password": c.Password})
	path := p.LoginPath
	if path == "" {
		path = "/auth/login"
	}
	req, err := http.NewRequestWithContext(ctx, "POST", a.base+path, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("login хүсэлт: %w", a.sanitize(err))
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("login HTTP %d", resp.StatusCode)
	}
	tok := findToken(data)
	if tok == "" {
		return "", fmt.Errorf("login хариунаас token олдсонгүй")
	}
	return tok, nil
}

func (a *authState) sanitize(err error) error { return fmt.Errorf("%s", a.red.String(err.Error())) }

// findToken — хариуны дээд түвшин эсвэл "data"/"result" доторх token|access_token|accessToken.
func findToken(data []byte) string {
	var m map[string]any
	if json.Unmarshal(data, &m) != nil {
		return ""
	}
	for _, scope := range []map[string]any{m, asMap(m["data"]), asMap(m["result"])} {
		for _, k := range []string{"token", "access_token", "accessToken", "jwt"} {
			if s, ok := scope[k].(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

func asMap(v any) map[string]any { m, _ := v.(map[string]any); return m }
