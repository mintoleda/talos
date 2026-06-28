package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"sync"

	"github.com/mintoleda/talos/internal/config"
	"github.com/mintoleda/talos/internal/loop"
	"github.com/mintoleda/talos/internal/models"
	"github.com/mintoleda/talos/internal/provider"
	"github.com/mintoleda/talos/internal/provider/openai"
	"github.com/mintoleda/talos/internal/session"
	"github.com/mintoleda/talos/internal/tui/dialogs"
)

// app holds the mutable runtime state shared by the interactive commands
// (/provider, /model, /login) and the operations over it. It exists so that
// modelCache — which fetchModels writes and switchProvider/saveLogin clear —
// has an explicit owner rather than being a local captured by several closures.
type app struct {
	cfg     *config.Config
	lp      *loop.Loop
	pb      *loop.PromptBuilder
	cwd     string
	noTools bool

	// modelCache holds the last-fetched model list for the active provider.
	// Cleared whenever the provider switches or a login is added so stale
	// lists don't linger.
	modelCache []models.Entry
}

func (a *app) newSession() (*session.Transcript, string, error) {
	ns := session.NewSession(a.cwd)
	ntx, err := session.Create(ns.Path)
	if err != nil {
		return nil, "", err
	}
	return ntx, ns.ID, nil
}

func (a *app) resumeSession(id string) (*session.Transcript, string, error) {
	var sess session.Session
	var err error
	if id != "" {
		sess, err = session.OpenSession(a.cwd, id)
		if err != nil {
			return nil, "", fmt.Errorf("session not found: %s", id)
		}
	} else {
		sess, err = pickSession(a.cwd)
		if err != nil {
			return nil, "", err
		}
	}
	tx, err := session.Load(sess.Path)
	if err != nil {
		return nil, "", fmt.Errorf("load session: %w", err)
	}
	return tx, sess.ID, nil
}

// switchProvider creates a new provider client, re-creates the compactor, and
// swaps them on the loop. Used by the /provider and /model commands.
func (a *app) switchProvider(pName, pModel string) error {
	oldProv := a.cfg.Provider
	oldModel := a.cfg.Model

	a.cfg.Provider = pName
	if pModel != "" {
		a.cfg.Model = pModel
	}
	if pName != oldProv {
		a.cfg.BaseURL = "" // let newProvider pick the canonical URL from the registry
	}
	a.cfg.ResolveAPIKey()

	prov, comp, err := newProvider(a.cfg, a.noTools)
	if err != nil {
		a.cfg.Provider = oldProv
		a.cfg.Model = oldModel
		return err
	}
	a.lp.SetProvider(prov)
	a.lp.SetCompactor(comp)
	a.pb.SetModel(a.cfg.Model)
	a.modelCache = nil // clear so next picker open re-fetches for the new provider

	// Persist the choice so it survives restarts.
	if err := config.SaveProviderModel(a.cfg.BaseDir, a.cfg.Provider, a.cfg.Model); err != nil {
		fmt.Fprintf(os.Stderr, "[warning] save provider/model: %v\n", err)
	}
	return nil
}

// fetchModels queries all logged-in providers concurrently and returns a
// combined, sorted model list. Results are cached until the provider switches
// or a login is added.
func (a *app) fetchModels() ([]models.Entry, error) {
	if a.modelCache != nil {
		return a.modelCache, nil
	}
	type result struct {
		entries []models.Entry
	}
	ch := make(chan result, len(provider.All))
	var wg sync.WaitGroup
	for _, kp := range provider.All {
		// Anthropic doesn't expose a /v1/models endpoint; skip it and fall
		// back to its hardcoded model list elsewhere.
		if kp.Name == "anthropic" {
			continue
		}
		key := config.ResolveKeyFor(a.cfg.BaseDir, kp.Name, kp.EnvVar)
		if key == "" {
			continue
		}
		wg.Add(1)
		go func(kp provider.Known, key string) {
			defer wg.Done()
			lister := openai.New(kp.BaseURL, key)
			ids, err := lister.ListModels(context.Background())
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

	var all []models.Entry
	for r := range ch {
		all = append(all, r.entries...)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Provider != all[j].Provider {
			return all[i].Provider < all[j].Provider
		}
		return all[i].ID < all[j].ID
	})
	a.modelCache = all
	return all, nil
}

func (a *app) loginProviders() []dialogs.LoginProvider {
	var out []dialogs.LoginProvider
	for _, kp := range provider.All {
		key := config.ResolveKeyFor(a.cfg.BaseDir, kp.Name, kp.EnvVar)
		out = append(out, dialogs.LoginProvider{
			Name:     kp.Name,
			Label:    kp.Label,
			LoggedIn: key != "",
		})
	}
	return out
}

func (a *app) fetchSessions() ([]dialogs.SessionEntry, error) {
	previews, err := session.ListSessionPreviews(a.cwd)
	if err != nil {
		return nil, err
	}
	entries := make([]dialogs.SessionEntry, len(previews))
	for i, p := range previews {
		entries[i] = dialogs.SessionEntry{ID: p.ID, ModTime: p.ModTime, Preview: p.Preview}
	}
	return entries, nil
}

func (a *app) saveLogin(provName, key string) error {
	if err := config.WriteAuthKey(a.cfg.BaseDir, provName, key); err != nil {
		return err
	}
	a.modelCache = nil
	return nil
}
