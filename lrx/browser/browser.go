package browser

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func LaunchBrowser(browserType string, proxyAddress string, customCertPath string, profileDir string, startURL string) (*exec.Cmd, error) {
	if browserType == "" {
		browserType = "firefox" // Default to Firefox (CamoFox is the supported pentest browser)
	}

	browserType = strings.ToLower(browserType)

	switch browserType {
	case "firefox":
		return launchFirefox(proxyAddress, customCertPath, profileDir)
	case "safari":
		return launchSafari(proxyAddress, customCertPath, profileDir)
	case "terminal":
		return launchTerminal(proxyAddress, customCertPath)
	default:
		return nil, fmt.Errorf("unsupported browser: %s", browserType)
	}
}

// copyFile is kept in this file as it's used by all browser implementations
func copyFile(src, dst string) error {
	input, err := os.ReadFile(src)
	if err != nil {
		return err
	}

	return os.WriteFile(dst, input, 0644)
}
