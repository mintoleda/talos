package pricing

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

const catalogURL = "https://pi.dev/models"

var (
	rowRe  = regexp.MustCompile(`(?s)<tr[^>]*data-model-row[^>]*>.*?</tr>`)
	idRe   = regexp.MustCompile(`data-model-id="([^"]*)"`)
	provRe = regexp.MustCompile(`data-model-provider="([^"]*)"`)
	ctxRe  = regexp.MustCompile(`data-label="Context"[^>]*>([^<]*)`)
	inRe   = regexp.MustCompile(`data-label="Input /M"[^>]*>([^<]*)`)
	outRe  = regexp.MustCompile(`data-label="Output /M"[^>]*>([^<]*)`)
)

// FetchLive fetches the live model catalog from pi.dev/models and returns it
// in the same compact format as data.json. Any network or parse failure
// returns a non-nil error; callers should fall back to cached or embedded data.
func FetchLive(ctx context.Context) (map[string]rawPrice, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, catalogURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return parseHTML(body)
}

func parseHTML(body []byte) (map[string]rawPrice, error) {
	rows := rowRe.FindAll(body, -1)
	if len(rows) == 0 {
		return nil, fmt.Errorf("no model rows found in response")
	}
	grab := func(re *regexp.Regexp, row []byte) string {
		m := re.FindSubmatch(row)
		if m == nil {
			return ""
		}
		return strings.TrimSpace(string(m[1]))
	}
	models := make(map[string]rawPrice, len(rows))
	for _, row := range rows {
		id := grab(idRe, row)
		prov := grab(provRe, row)
		if id == "" || prov == "" {
			continue
		}
		inp := parsePrice(grab(inRe, row))
		out := parsePrice(grab(outRe, row))
		ctx := parseCtx(grab(ctxRe, row))
		if inp == 0 && out == 0 && ctx == 0 {
			continue
		}
		models[prov+"/"+id] = rawPrice{I: inp, O: out, C: ctx}
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("parsed 0 models — page format may have changed")
	}
	return models, nil
}

func MarshalRaw(m map[string]rawPrice) ([]byte, error) {
	return json.Marshal(m)
}

func parsePrice(s string) float64 {
	s = strings.TrimPrefix(strings.TrimSpace(s), "$")
	s = strings.ReplaceAll(s, ",", "")
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

func parseCtx(s string) int {
	s = strings.ReplaceAll(strings.TrimSpace(s), ",", "")
	n, _ := strconv.Atoi(s)
	return n
}
