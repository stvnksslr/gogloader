package gog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Public OAuth client credentials used by the GOG Galaxy client. These are
// widely used by third-party GOG tools (lgogdownloader, heroic, minigalaxy).
const (
	clientID     = "46899977096215655"
	clientSecret = "9d85c43b1482497dbbce61f6e4aa173a433796eeae2ca8c5f6129f2dc4de46d9"
	redirectURI  = "https://embed.gog.com/on_login_success?origin=client"
	authBase     = "https://auth.gog.com"
)

// ErrNotLoggedIn is returned when no stored token exists.
var ErrNotLoggedIn = errors.New("not logged in: run `gogloader auth login` first")

// Token is an OAuth token as returned by auth.gog.com.
type Token struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresIn    int64     `json:"expires_in"`
	UserID       string    `json:"user_id"`
	ExpiresAt    time.Time `json:"expires_at"`
}

func (t *Token) expired() bool {
	return t.AccessToken == "" || time.Now().Add(60*time.Second).After(t.ExpiresAt)
}

// LoginURL is the URL the user opens in a browser to authenticate.
func LoginURL() string {
	q := url.Values{
		"client_id":     {clientID},
		"redirect_uri":  {redirectURI},
		"response_type": {"code"},
		"layout":        {"client2"},
	}
	return authBase + "/auth?" + q.Encode()
}

// ExtractCode accepts either a bare authorization code or the full
// on_login_success URL the browser lands on, and returns the code.
func ExtractCode(input string) (string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", errors.New("empty input")
	}
	if strings.Contains(input, "://") || strings.Contains(input, "code=") {
		u, err := url.Parse(input)
		if err != nil {
			return "", fmt.Errorf("parse url: %w", err)
		}
		if c := u.Query().Get("code"); c != "" {
			return c, nil
		}
		return "", errors.New("no `code` parameter found in URL")
	}
	return input, nil
}

func loadToken() (*Token, error) {
	b, err := loadTokenBytes()
	if errors.Is(err, errNoToken) {
		return nil, ErrNotLoggedIn
	}
	if err != nil {
		return nil, err
	}
	var t Token
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, fmt.Errorf("stored token in %s is corrupt (run `gogloader auth login`): %w", TokenLocation(), err)
	}
	return &t, nil
}

func saveToken(t *Token, announce bool) error {
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return saveTokenBytes(b, announce)
}

// Logout removes the stored token from every backend and clears the cache.
// GOG has no public revocation endpoint, so the token is only forgotten
// locally.
func Logout() error {
	return errors.Join(removeStoredToken(), ClearCache())
}

// Login exchanges an authorization code for a token and stores it.
func Login(ctx context.Context, code string) (*Token, error) {
	t, err := requestToken(ctx, url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {code},
		"redirect_uri": {redirectURI},
	})
	if err != nil {
		return nil, err
	}
	return t, saveToken(t, true)
}

func refresh(ctx context.Context, t *Token) (*Token, error) {
	if t.RefreshToken == "" {
		return nil, ErrNotLoggedIn
	}
	nt, err := requestToken(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {t.RefreshToken},
	})
	if err != nil {
		return nil, fmt.Errorf("refresh token (try `gogloader auth login` again): %w", err)
	}
	return nt, saveToken(nt, false)
}

func requestToken(ctx context.Context, params url.Values) (*Token, error) {
	params.Set("client_id", clientID)
	params.Set("client_secret", clientSecret)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, authBase+"/token?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := apiHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var body struct {
		Token
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode token response (HTTP %d): %w", resp.StatusCode, err)
	}
	if resp.StatusCode != http.StatusOK || body.AccessToken == "" {
		return nil, fmt.Errorf("token request failed (HTTP %d): %s %s", resp.StatusCode, body.Error, body.Description)
	}
	t := body.Token
	t.ExpiresAt = time.Now().Add(time.Duration(t.ExpiresIn) * time.Second)
	return &t, nil
}
