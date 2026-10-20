package suite

import (
	"fmt"

	"github.com/T-Crypt/berth/internal/config"
)

// Merged reads the berth.toml at path like config.Check does, then appends
// one derived engine per suite install after the file's engines. A file
// engine whose name equals an install's is dropped and reported as a
// warning: the suite is the source of truth for what it manages. A missing
// state file changes nothing. The merged config is validated before it is
// returned, so a caller that trusts the error can trust the config too.
func Merged(path string) (config.Config, []config.Warning, error) {
	statePath := SuitePath(path)
	cfg, warns, err := config.Check(path)
	if err != nil {
		return cfg, warns, err
	}
	st, err := Load(statePath)
	if err != nil {
		return cfg, warns, fmt.Errorf("%s: %w", statePath, err)
	}
	if err := st.Validate(); err != nil {
		return cfg, warns, fmt.Errorf("%s: %w", statePath, err)
	}
	for _, in := range st.Installs {
		eng := Engine(in)
		for i, e := range cfg.Engine {
			if e.Name != in.Name {
				continue
			}
			cfg.Engine = append(cfg.Engine[:i], cfg.Engine[i+1:]...)
			warns = append(warns, config.Warning{
				Key:     fmt.Sprintf("engine[%d]", i),
				Message: fmt.Sprintf("engine %q is managed by the suite (%s); the file entry is dropped", in.Name, statePath),
			})
			break
		}
		cfg.Engine = append(cfg.Engine, eng)
	}
	if err := cfg.Validate(); err != nil {
		return cfg, warns, fmt.Errorf("%s: %w", statePath, err)
	}
	return cfg, warns, nil
}
