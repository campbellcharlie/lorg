package config

import (
	"fmt"
	"os"
	"path"

	"github.com/campbellcharlie/lorg/internal/utils"
)

type Config struct {
	HostAddr string

	HomeDirectory     string // User's home directory
	ConfigDirectory   string // Config directory
	ProjectsDirectory string // Projects directory
	CacheDirectory    string // Cache directory
	TemplateDirectory string // Template directory

	ProjectID   string //  Active Project's ID
	CWDirectory string //  Projects Directory + ProjectID
}

func (c *Config) Initiate() {
	var err error

	// Probably not used
	c.HomeDirectory, err = os.UserHomeDir()
	utils.CheckErr("", err)

	c.CacheDirectory, err = os.UserCacheDir()
	c.CacheDirectory = path.Join(c.CacheDirectory, "lorg")
	os.MkdirAll(c.CacheDirectory, 0700)
	utils.CheckErr("", err)

	c.ProjectsDirectory, err = os.UserConfigDir()
	c.ProjectsDirectory = path.Join(c.ProjectsDirectory, "lorg")
	os.MkdirAll(c.ProjectsDirectory, 0700)
	utils.CheckErr("", err)

	c.ConfigDirectory = path.Join(c.HomeDirectory, ".config", "lorg")
	os.MkdirAll(c.ConfigDirectory, 0700)

	// Write intercepted.html to ProjectsDirectory on every start
	interceptedPath := path.Join(c.ProjectsDirectory, "intercepted.html")
	go os.WriteFile(interceptedPath, []byte(defaultInterceptedHTML), 0644)

	// Default to localhost binding for security — prevents network exposure
	if c.HostAddr == "" {
		c.HostAddr = "127.0.0.1:8090"
	}
}

// InterceptedPagePath returns the file:// URL for the intercepted landing page.
func (c *Config) InterceptedPagePath() string {
	return "file://" + path.Join(c.ProjectsDirectory, "intercepted.html")
}

func (c *Config) ShowConfig() {
	fmt.Println("Home:         ", c.HomeDirectory)
	fmt.Println("Cache:        ", c.CacheDirectory)
	fmt.Println("Config:       ", c.ProjectsDirectory)
}
