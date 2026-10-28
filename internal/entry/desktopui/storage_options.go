package desktopui

import (
	"path/filepath"

	"github.com/hansshaw/AInoval-web-ui/assets"
	"github.com/hansshaw/AInoval-web-ui/internal/host"
)

func (s *Server) assetLoadOptions(project string) assets.LoadOptions {
	if s.dataDir == "" {
		return assets.DefaultLoadOptions(project)
	}
	opts := assets.LoadOptions{HomeStyleDir: filepath.Join(s.dataDir, "style")}
	if project != "" {
		opts.BookStyleDir = filepath.Join(project, "style")
	}
	return opts
}

func (s *Server) hostOptions() []host.NewOption {
	if s.dataDir == "" {
		return nil
	}
	return []host.NewOption{host.WithUserDataDir(s.dataDir)}
}

func (s *Server) legacyProjectDirectory() string {
	if s.dataDir != "" {
		return ""
	}
	legacy, _ := filepath.Abs(filepath.Join("output", "novel"))
	return legacy
}
