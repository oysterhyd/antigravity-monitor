package history

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/oysterhyd/antigravity-monitor/internal/config"
	"github.com/oysterhyd/antigravity-monitor/internal/metrics"
)

const cacheSchemaVersion = 1

type cacheFile struct {
	Version       int                    `json:"version"`
	PricingHash   string                 `json:"pricing_hash"`
	Conversations map[string]*cachedConv `json:"conversations"`
}

type cachedConv struct {
	ConvID     string          `json:"conv_id"`
	MtimeNs    int64           `json:"mtime_ns"`
	Size       int64           `json:"size"`
	DBMtimeNs  int64           `json:"db_mtime_ns,omitempty"`
	DBSize     int64           `json:"db_size,omitempty"`
	WalMtimeNs int64           `json:"wal_mtime_ns,omitempty"`
	WalSize    int64           `json:"wal_size,omitempty"`
	RootConvID string          `json:"root_conv_id,omitempty"`
	Requests   []cachedRequest `json:"requests,omitempty"`
}

type cachedRequest struct {
	TSUnixMs      int64             `json:"ts"`
	TSUnixNs      int64             `json:"ts_ns,omitempty"`
	SubModel      string            `json:"m"`
	Pool          metrics.ModelPool `json:"p"`
	InputTokens   int64             `json:"in,omitempty"`
	CacheReadTok  int64             `json:"cr,omitempty"`
	CacheWriteTok int64             `json:"cw,omitempty"`
	OutputTokens  int64             `json:"out,omitempty"`
	CostUSD       float64           `json:"cost,omitempty"`
	CacheSavedUSD float64           `json:"saved,omitempty"`
}

func (r cachedRequest) timestamp() time.Time {
	if r.TSUnixNs > 0 {
		return time.Unix(0, r.TSUnixNs).UTC()
	}
	if r.TSUnixMs > 0 {
		return time.UnixMilli(r.TSUnixMs).UTC()
	}
	return time.Time{}
}

func (r cachedRequest) totalTokens() int64 {
	return r.CacheReadTok + r.InputTokens + r.CacheWriteTok + r.OutputTokens
}

type convFileStat struct {
	effectiveMtimeNs int64
	effectiveSize    int64
	dbMtimeNs        int64
	dbSize           int64
	walMtimeNs       int64
	walSize          int64
}

func statConvDB(dbPath string) (convFileStat, error) {
	dbInfo, err := os.Stat(dbPath)
	if err != nil {
		return convFileStat{}, err
	}
	return statConvDBWithInfo(dbPath, dbInfo), nil
}

func statConvDBWithInfo(dbPath string, dbInfo os.FileInfo) convFileStat {
	st := convFileStat{
		dbMtimeNs: dbInfo.ModTime().UnixNano(),
		dbSize:    dbInfo.Size(),
	}
	st.effectiveMtimeNs = st.dbMtimeNs
	st.effectiveSize = st.dbSize

	if walInfo, err := os.Stat(dbPath + "-wal"); err == nil && walInfo.Size() > 0 {
		st.walMtimeNs = walInfo.ModTime().UnixNano()
		st.walSize = walInfo.Size()
		if st.walMtimeNs > st.effectiveMtimeNs {
			st.effectiveMtimeNs = st.walMtimeNs
		}
		st.effectiveSize += st.walSize
	}
	return st
}

func (c *cachedConv) matches(st convFileStat) bool {
	if c == nil {
		return false
	}
	return c.MtimeNs == st.effectiveMtimeNs &&
		c.Size == st.effectiveSize &&
		c.DBMtimeNs == st.dbMtimeNs &&
		c.DBSize == st.dbSize &&
		c.WalMtimeNs == st.walMtimeNs &&
		c.WalSize == st.walSize
}

// DefaultCachePath returns the default incremental usage history cache path:
// filepath.Join(filepath.Dir(config.DefaultPath()), "usage_cache.json").
func DefaultCachePath() string {
	if cfgPath, err := config.DefaultPath(); err == nil && strings.TrimSpace(cfgPath) != "" {
		return filepath.Join(filepath.Dir(cfgPath), "usage_cache.json")
	}
	if dir, err := os.UserConfigDir(); err == nil && strings.TrimSpace(dir) != "" {
		return filepath.Join(dir, "antigravity-monitor", "usage_cache.json")
	}
	return filepath.Join(os.TempDir(), "antigravity-monitor", "usage_cache.json")
}

func computePricingHash(calc *metrics.Calculator) string {
	if calc == nil {
		return ""
	}
	raw, err := json.Marshal(calc.Config())
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func newEmptyCache(pricingHash string) cacheFile {
	return cacheFile{
		Version:       cacheSchemaVersion,
		PricingHash:   pricingHash,
		Conversations: make(map[string]*cachedConv),
	}
}

func loadCache(cachePath, pricingHash string) (cacheFile, bool) {
	if strings.TrimSpace(cachePath) == "" {
		return newEmptyCache(pricingHash), false
	}
	raw, err := os.ReadFile(cachePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return newEmptyCache(pricingHash), false
		}
		return newEmptyCache(pricingHash), true
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return newEmptyCache(pricingHash), true
	}
	var cf cacheFile
	if err := json.Unmarshal(raw, &cf); err != nil {
		return newEmptyCache(pricingHash), true
	}
	if cf.Version != cacheSchemaVersion || cf.PricingHash != pricingHash {
		return newEmptyCache(pricingHash), true
	}
	if cf.Conversations == nil {
		cf.Conversations = make(map[string]*cachedConv)
	}
	intern := make(map[string]string, 16)
	for _, conv := range cf.Conversations {
		if conv == nil {
			continue
		}
		for i := range conv.Requests {
			m := conv.Requests[i].SubModel
			if m == "" {
				continue
			}
			if cached, ok := intern[m]; ok {
				conv.Requests[i].SubModel = cached
			} else {
				intern[m] = m
			}
		}
	}
	return cf, false
}

func saveCacheAtomic(cachePath string, cf cacheFile) error {
	if strings.TrimSpace(cachePath) == "" {
		return nil
	}
	dir := filepath.Dir(cachePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create usage cache dir %q: %w", dir, err)
	}

	payload, err := json.Marshal(cf)
	if err != nil {
		return fmt.Errorf("marshal usage cache: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".usage-cache-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp usage cache file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp usage cache file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp usage cache file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp usage cache file: %w", err)
	}

	if err := os.Rename(tmpName, cachePath); err != nil {
		return fmt.Errorf("replace usage cache %q: %w", cachePath, err)
	}
	cleanup = false
	return nil
}
