package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/redtidev1918/releasegraph/internal/credential"
	"github.com/redtidev1918/releasegraph/internal/domain"
	"github.com/redtidev1918/releasegraph/internal/fleet"
	"github.com/redtidev1918/releasegraph/internal/github"
)

// fleetCommand implements the two fleet entry points:
//
//	releasegraph fleet audit    --manifest fleet.yaml     # authority (manifest)
//	releasegraph fleet discover --owner someone           # candidates only
//
// A bare `releasegraph fleet` behaves like `fleet discover` for compatibility.
func fleetCommand(w io.Writer, args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "audit", "discover":
			return fleetSubcommand(w, args[0], args[1:])
		}
	}
	return fleetSubcommand(w, "discover", args)
}

func fleetSubcommand(w io.Writer, sub string, args []string) error {
	var format, owner, manifestPath string
	var publicOnly bool
	fs := flags(&format)
	fs.StringVar(&owner, "owner", "", "repository owner (discovery input only)")
	fs.StringVar(&manifestPath, "manifest", "fleet.yaml", "fleet manifest path")
	fs.BoolVar(&publicOnly, "public-only", false, "list public repositories only")
	if err := fs.Parse(args); err != nil {
		return err
	}

	switch sub {
	case "audit":
		return fleetAudit(w, format, manifestPath, owner, publicOnly)
	default:
		return fleetDiscover(w, format, owner, publicOnly)
	}
}

// fleetAudit reports the authoritative managed set from fleet.yaml. GitHub data
// is metadata enrichment (visibility, default branch, archived) and can never
// add a repository to the fleet.
func fleetAudit(w io.Writer, format, manifestPath, owner string, publicOnly bool) error {
	manifest, err := fleet.LoadManifest(manifestPath)
	if err != nil {
		return err
	}

	managed, unmanaged := fleet.Resolve(manifest, nil)
	enriched := false
	if owner != "" {
		if err := credential.RequireFleet("fleet audit --owner (metadata enrichment)"); err != nil {
			// Enrichment is optional: report the manifest authority regardless.
			fmt.Fprintf(os.Stderr, "note: %v; reporting manifest authority without GitHub enrichment\n", err)
		} else if discovered, err := fleet.Discover(context.Background(), github.New().Bind(domain.ExecutionContext{
			Scope:           domain.ScopeFleet,
			Actor:           actorName(),
			CredentialClass: domain.CredentialFleet,
		}), owner, publicOnly); err == nil {
			managed, unmanaged = fleet.Resolve(manifest, discovered.Repositories)
			enriched = true
		} else {
			fmt.Fprintf(os.Stderr, "note: discovery failed (%v); reporting manifest authority\n", err)
		}
	}

	out := map[string]any{
		"manifest":  manifestPath,
		"authority": "fleet.yaml",
		"enriched":  enriched,
		"managed":   managed,
		"operable":  fleet.Operable(managed),
		"unmanaged": unmanaged,
	}
	if canary, ok := manifest.Canary(); ok {
		out["canary"] = canary
	}
	return write(w, format, out, func(w io.Writer, _ any) {
		fmt.Fprintf(w, "fleet manifest: %s (authority)\n", manifestPath)
		if canary, ok := manifest.Canary(); ok {
			fmt.Fprintf(w, "canary:         %s\n", canary)
		}
		for _, repo := range managed {
			marker := ""
			if repo.Canary {
				marker = " [canary]"
			}
			fmt.Fprintf(w, "  %-42s %s%s\n", repo.Name, repo.Classification, marker)
		}
		if len(unmanaged) > 0 {
			fmt.Fprintf(w, "discovered but NOT managed (%d) — informational only:\n", len(unmanaged))
			for _, repo := range unmanaged {
				fmt.Fprintf(w, "  %-42s %s\n", repo.Name, repo.Classification)
			}
		}
	})
}

// fleetDiscover lists repositories that exist under an owner. It is an input for
// enrichment and manifest review; it never grants ownership.
func fleetDiscover(w io.Writer, format, owner string, publicOnly bool) error {
	if owner == "" {
		return fmt.Errorf("--owner is required for fleet discover")
	}
	if err := credential.RequireFleet("fleet discover"); err != nil {
		return err
	}
	discovered, err := fleet.Discover(context.Background(), github.New().Bind(domain.ExecutionContext{
		Scope:           domain.ScopeFleet,
		Actor:           actorName(),
		CredentialClass: domain.CredentialFleet,
	}), owner, publicOnly)
	if err != nil {
		return err
	}
	candidates := []fleet.Resolved{}
	for _, repo := range discovered.Repositories {
		candidates = append(candidates, fleet.Resolved{
			Name:           repo.Name,
			Classification: fleet.ClassificationDiscoveredUnmanaged,
			Archived:       repo.Archived,
			Visibility:     repo.Visibility,
			DefaultBranch:  repo.DefaultBranch,
		})
	}
	out := map[string]any{"owner": owner, "candidates": candidates, "authority": "none (candidates only)"}
	return write(w, format, out, func(w io.Writer, _ any) {
		fmt.Fprintf(w, "candidates under %s (not managed until declared in fleet.yaml):\n", owner)
		for _, repo := range candidates {
			fmt.Fprintf(w, "  %-42s %s\n", repo.Name, repo.Classification)
		}
	})
}
