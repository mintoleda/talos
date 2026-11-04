package stack

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/mintoleda/talos/internal/config"
	"github.com/mintoleda/talos/internal/loop"
	"github.com/mintoleda/talos/internal/memory"
	"github.com/mintoleda/talos/internal/models"
	"github.com/mintoleda/talos/internal/pricing"
	"github.com/mintoleda/talos/internal/provider"
	"github.com/mintoleda/talos/internal/provider/anthropic"
	"github.com/mintoleda/talos/internal/provider/openai"
)

// ListModels lists models from every provider with a configured key.
// Providers that fail or outlive ctx are skipped.
func ListModels(ctx context.Context, baseDir string) []models.Entry {
	type result struct {
		entries []models.Entry
	}
	ch := make(chan result, len(provider.All))
	var wg sync.WaitGroup
	for _, kp := range provider.All {
		key := config.ResolveKeyFor(baseDir, kp.Name, kp.EnvVar)
		if key == "" {
			continue
		}
		wg.Add(1)
		go func(kp provider.Known, key string) {
			defer wg.Done()
			var lister provider.Provider
			if kp.Name == "anthropic" {
				lister = anthropic.New(kp.BaseURL, key, anthropic.Config{})
			} else {
				lister = openai.New(kp.BaseURL, key)
			}
			ids, err := lister.ListModels(ctx)
			if err != nil {
				return
			}
			entries := make([]models.Entry, len(ids))
			for i, id := range ids {
				entries[i] = models.Entry{Provider: kp.Name, ID: id}
			}
			ch <- result{entries: entries}
		}(kp, key)
	}
	go func() { wg.Wait(); close(ch) }()

	all := make([]models.Entry, 0)
	for r := range ch {
		all = append(all, r.entries...)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Provider != all[j].Provider {
			return all[i].Provider < all[j].Provider
		}
		return all[i].ID < all[j].ID
	})

	return all
}

// SwitchModel rebuilds the provider and compactor for cfg (a per-session
// copy) and installs them on lp/pb. On error cfg is restored. warn reports a
// non-fatal problem (historian disabled) the caller should surface.
func SwitchModel(cfg *config.Config, lp *loop.Loop, pb *loop.PromptBuilder, prices *pricing.Table, memStore *memory.Store, noTools bool, pName, pModel string) (warn, err error) {
	oldProv, oldModel, oldBase := cfg.Provider, cfg.Model, cfg.BaseURL

	cfg.Provider = pName
	if pModel != "" {
		cfg.Model = pModel
	}
	if pName != oldProv {
		cfg.BaseURL = ""
	}
	cfg.ResolveAPIKey()

	prov, comp, err := NewProvider(cfg, noTools)
	if err != nil {
		cfg.Provider, cfg.Model, cfg.BaseURL = oldProv, oldModel, oldBase
		cfg.ResolveAPIKey()
		return nil, err
	}
	lp.SetProvider(prov)
	if comp != nil && cfg.Historian && memStore != nil {
		hist, herr := NewHistorian(cfg, memStore)
		if herr != nil {
			warn = fmt.Errorf("historian disabled: %w", herr)
		} else {
			comp.Historian = hist
		}
	}
	lp.SetCompactor(comp)
	pb.SetModel(cfg.Model)
	if prices != nil {
		pb.SetContextLimit(prices.ContextWindow(cfg.Model))
	}
	return warn, nil
}
