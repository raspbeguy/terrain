package config

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/raspbeguy/terrain/internal/backend/local"
	"github.com/raspbeguy/terrain/internal/backend/remote"
	"github.com/raspbeguy/terrain/internal/domain"
)

// Per-backend failures are logged but don't abort the rest of the registry.
func BuildBackends(c *Config) ([]domain.Backend, error) {
	if c == nil {
		return nil, nil
	}
	backends := make([]domain.Backend, 0, len(c.Backends))
	for _, bc := range c.Backends {
		switch bc.Type {
		case "local":
			b := local.New(bc.ID, bc.Name)
			b.SetRuntimeDefaults(local.RuntimeDefaults{Engine: c.App.DefaultEngine})
			for _, p := range bc.Projects {
				b.AddProject(local.Project{
					ID:          p.ID,
					Name:        p.Name,
					GitURL:      p.GitURL,
					GitRef:      p.GitRef,
					Subpath:     p.Subpath,
					SSHKeyLabel: p.SSHKeyLabel,
					GitUsername: p.GitUsername,
				})
			}
			backends = append(backends, b)

		case "remote":
			b, err := remote.New(remote.Config{
				ID:           bc.ID,
				Name:         bc.Name,
				Flavor:       remote.Flavor(bc.Flavor),
				Endpoint:     bc.Endpoint,
				Organization: bc.Organization,
				Token:        bc.ResolveToken(),
			})
			if err != nil {
				slog.Warn("skip remote backend", "id", bc.ID, "err", err)
				continue
			}
			go b.Probe(context.Background())
			backends = append(backends, b)

		default:
			return nil, fmt.Errorf("unknown backend type %q in entry %q", bc.Type, bc.ID)
		}
	}
	return backends, nil
}
