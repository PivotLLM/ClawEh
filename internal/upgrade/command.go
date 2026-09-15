// Package upgrade provides the `claw upgrade` subcommand, which downloads
// the latest release binary from GitHub, verifies its SHA256 checksum,
// atomically updates the running executable, and restarts any active background service.
package upgrade

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/PivotLLM/ClawEh/app"
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/internal"
	"github.com/PivotLLM/ClawEh/internal/install"
)

const (
	repoOwner   = "PivotLLM"
	repoName    = "ClawEh"
	apiBaseURL  = "https://api.github.com/repos/" + repoOwner + "/" + repoName
	httpTimeout = 60 * time.Second
)

// GitHubRelease represents the GitHub releases API response.
type GitHubRelease struct {
	TagName string        `json:"tag_name"`
	Name    string        `json:"name"`
	Assets  []GitHubAsset `json:"assets"`
}

// GitHubAsset represents an asset attached to a release.
type GitHubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

// NewUpgradeCommand returns the `claw upgrade` subcommand.
func NewUpgradeCommand() *cobra.Command {
	var (
		checkOnly     bool
		force         bool
		targetVersion string
		yes           bool
	)

	cmd := &cobra.Command{
		Use:     "upgrade",
		Aliases: []string{"update"},
		Short:   "Download the latest release binary from GitHub and install it",
		Long: "Checks for the latest release on GitHub (" + repoOwner + "/" + repoName + "),\n" +
			"verifies the SHA256 checksum of the archive, atomically replaces the currently\n" +
			"running binary, and restarts the background service if one is active.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runUpgrade(checkOnly, force, targetVersion, yes)
		},
	}

	cmd.Flags().BoolVar(&checkOnly, "check", false, "Check for available updates without downloading or installing.")
	cmd.Flags().BoolVar(&force, "force", false, "Force download and reinstall even if already running the target version.")
	cmd.Flags().StringVar(&targetVersion, "version", "", "Install a specific version tag instead of the latest release.")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Skip interactive confirmation prompt.")

	return cmd
}

func runUpgrade(checkOnly, force bool, targetVersion string, autoYes bool) error {
	currentVer := app.SemVer()
	fmt.Printf("Current %s version: v%s\n", app.Name(), currentVer)
	fmt.Println("Checking for releases on GitHub...")

	release, err := fetchRelease(targetVersion)
	if err != nil {
		return fmt.Errorf("fetching release information: %w", err)
	}

	latestVer := strings.TrimPrefix(release.TagName, "v")
	cmp := compareSemVer(currentVer, latestVer)

	if checkOnly {
		if cmp < 0 {
			fmt.Printf("A newer version of %s is available: v%s -> v%s\n", app.Name(), currentVer, latestVer)
			fmt.Printf("Run `%s upgrade` to install it.\n", internal.BinaryName)
		} else if cmp > 0 {
			fmt.Printf("%s is running a newer version than latest release (v%s vs release v%s).\n", app.Name(), currentVer, latestVer)
		} else {
			fmt.Printf("%s is already up to date (v%s).\n", app.Name(), currentVer)
		}
		return nil
	}

	if cmp >= 0 && !force && targetVersion == "" {
		fmt.Printf("%s is already up to date (v%s).\nUse `%s upgrade --force` to reinstall.\n", app.Name(), currentVer, internal.BinaryName)
		return nil
	}

	// Match archive and checksum assets
	archiveName := fmt.Sprintf("claw-%s-%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	checksumName := archiveName + ".sha256"

	var archiveAsset, checksumAsset *GitHubAsset
	for i := range release.Assets {
		asset := &release.Assets[i]
		if asset.Name == archiveName {
			archiveAsset = asset
		} else if asset.Name == checksumName {
			checksumAsset = asset
		}
	}

	if archiveAsset == nil {
		return fmt.Errorf("no release binary found for platform %s/%s (%s)", runtime.GOOS, runtime.GOARCH, archiveName)
	}

	// Locate running executable
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locating current executable: %w", err)
	}
	if resolved, rerr := filepath.EvalSymlinks(exePath); rerr == nil {
		exePath = resolved
	}

	// Check if an existing installation exists on the system
	homeDir, _ := os.UserHomeDir()
	existing := install.DetectExistingInstall(homeDir)
	if existing != nil && existing.BinaryPath != "" && (isBuildOrTempDir(exePath) || !fileExists(exePath)) {
		fmt.Printf("Detected installed %s at: %s\n", app.Name(), existing.BinaryPath)
		exePath = existing.BinaryPath
	}

	// Set CLAW_HOME if custom install path is detected
	if existing != nil && existing.ClawHome != "" {
		_ = os.Setenv(global.EnvVarHome, existing.ClawHome)
		_ = os.Setenv("CLAW_HOME", existing.ClawHome)
	} else if strings.HasPrefix(exePath, "/opt/claw") {
		_ = os.Setenv(global.EnvVarHome, "/opt/claw")
		_ = os.Setenv("CLAW_HOME", "/opt/claw")
	}

	// Verify write permission on target executable directory
	binDir := filepath.Dir(exePath)
	if err := checkDirWritable(binDir); err != nil {
		return fmt.Errorf("permission denied writing to %s.\nPlease run with sudo: sudo %s upgrade", binDir, internal.BinaryName)
	}

	// Summary & confirmation
	fmt.Printf("\n%s Upgrade Summary:\n", app.Name())
	fmt.Printf("  Current Version: v%s\n", currentVer)
	fmt.Printf("  Target Version:  v%s (tag: %s)\n", latestVer, release.TagName)
	fmt.Printf("  Release Asset:   %s\n", archiveAsset.Name)
	fmt.Printf("  Target Binary:   %s\n", exePath)
	if existing != nil && existing.ClawHome != "" {
		fmt.Printf("  Data Directory:  %s\n", existing.ClawHome)
	} else if strings.HasPrefix(exePath, "/opt/claw") {
		fmt.Printf("  Data Directory:  %s\n", "/opt/claw")
	}
	if existing != nil && existing.ServicePath != "" {
		activeStr := "inactive"
		if existing.IsActive {
			activeStr = "active"
		}
		fmt.Printf("  Existing Service: %s (%s, %s)\n", existing.ServicePath, existing.ServiceType, activeStr)
	}
	fmt.Println()

	if !autoYes {
		confirmed, err := confirmPrompt("Do you want to proceed with the upgrade?")
		if err != nil || !confirmed {
			fmt.Println("Upgrade cancelled.")
			return nil
		}
	}

	// Create temp directory for download
	tmpDir, err := os.MkdirTemp("", "claw-upgrade-*")
	if err != nil {
		return fmt.Errorf("creating temp directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Download archive
	archivePath := filepath.Join(tmpDir, archiveName)
	fmt.Printf("Downloading %s...\n", archiveAsset.Name)
	if err := downloadFile(archiveAsset.BrowserDownloadURL, archivePath); err != nil {
		return fmt.Errorf("downloading %s: %w", archiveAsset.Name, err)
	}

	// Verify checksum if sha256 asset is available
	if checksumAsset != nil {
		fmt.Printf("Verifying SHA256 checksum...\n")
		checksumPath := filepath.Join(tmpDir, checksumName)
		if err := downloadFile(checksumAsset.BrowserDownloadURL, checksumPath); err != nil {
			return fmt.Errorf("downloading checksum: %w", err)
		}

		expectedHash, err := readExpectedChecksum(checksumPath)
		if err != nil {
			return fmt.Errorf("reading checksum file: %w", err)
		}

		actualHash, err := computeSHA256(archivePath)
		if err != nil {
			return fmt.Errorf("calculating archive checksum: %w", err)
		}

		if !strings.EqualFold(expectedHash, actualHash) {
			return fmt.Errorf("SHA256 checksum mismatch!\n  Expected: %s\n  Actual:   %s\nThe download may be corrupted or incomplete.", expectedHash, actualHash)
		}
		fmt.Println("Checksum verified.")
	} else {
		fmt.Println("Warning: No .sha256 asset found in release; skipping checksum verification.")
	}

	// Extract binary from tar.gz
	fmt.Println("Extracting binary...")
	extractedBinPath := filepath.Join(tmpDir, "extracted-claw")
	extractedAuthPath := filepath.Join(tmpDir, "extracted-claw-auth")
	hasAuth, err := extractBinariesFromTarGz(archivePath, extractedBinPath, extractedAuthPath)
	if err != nil {
		return fmt.Errorf("extracting binary from archive: %w", err)
	}

	// Atomically replace executable
	fmt.Printf("Replacing %s...\n", exePath)
	if err := atomicReplace(extractedBinPath, exePath); err != nil {
		return fmt.Errorf("replacing binary at %s: %w", exePath, err)
	}

	// If claw-auth was in the archive and exists alongside claw, update it too
	authDest := filepath.Join(binDir, "claw-auth")
	if hasAuth && fileExists(authDest) {
		if err := atomicReplace(extractedAuthPath, authDest); err == nil {
			fmt.Printf("Updated companion tool: %s\n", authDest)
		}
	}

	fmt.Printf("Successfully upgraded %s to v%s!\n", app.Name(), latestVer)

	// Restart active background service
	restartActiveService()

	return nil
}

func fetchRelease(version string) (*GitHubRelease, error) {
	client := &http.Client{Timeout: httpTimeout}

	url := apiBaseURL + "/releases/latest"
	if version != "" {
		tag := version
		if !strings.HasPrefix(tag, "v") {
			tag = "v" + tag
		}
		url = apiBaseURL + "/releases/tags/" + tag
	}

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", fmt.Sprintf("ClawEh/%s (%s; %s)", app.SemVer(), runtime.GOOS, runtime.GOARCH))

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	// Fallback without "v" prefix if 404 when querying specific version
	if resp.StatusCode == http.StatusNotFound && version != "" && strings.HasPrefix(url, apiBaseURL+"/releases/tags/v") {
		bareURL := apiBaseURL + "/releases/tags/" + strings.TrimPrefix(version, "v")
		if bareReq, bErr := http.NewRequest(http.MethodGet, bareURL, nil); bErr == nil {
			bareReq.Header.Set("Accept", "application/vnd.github.v3+json")
			bareReq.Header.Set("User-Agent", req.Header.Get("User-Agent"))
			if bareResp, brErr := client.Do(bareReq); brErr == nil && bareResp.StatusCode == http.StatusOK {
				defer func() { _ = bareResp.Body.Close() }()
				var rel GitHubRelease
				if decErr := json.NewDecoder(bareResp.Body).Decode(&rel); decErr == nil {
					return &rel, nil
				}
			}
		}
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub API returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var rel GitHubRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, fmt.Errorf("parsing release JSON: %w", err)
	}
	return &rel, nil
}

func downloadFile(url, dstPath string) error {
	client := &http.Client{Timeout: httpTimeout}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", fmt.Sprintf("ClawEh/%s", app.SemVer()))

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d downloading %s", resp.StatusCode, url)
	}

	out, err := os.Create(dstPath)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()

	_, err = io.Copy(out, resp.Body)
	return err
}

func computeSHA256(filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func readExpectedChecksum(checksumFilePath string) (string, error) {
	data, err := os.ReadFile(checksumFilePath)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return "", fmt.Errorf("checksum file %s is empty", checksumFilePath)
	}
	return fields[0], nil
}

func extractBinariesFromTarGz(archivePath, clawDst, clawAuthDst string) (hasAuth bool, err error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return false, err
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	foundClaw := false

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return false, err
		}

		cleanName := filepath.Base(hdr.Name)
		if cleanName == "claw" && (hdr.Typeflag == tar.TypeReg || hdr.Typeflag == tar.TypeRegA) {
			out, err := os.OpenFile(clawDst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
			if err != nil {
				return false, err
			}
			if _, err := io.Copy(out, tr); err != nil {
				_ = out.Close()
				return false, err
			}
			_ = out.Close()
			foundClaw = true
		} else if cleanName == "claw-auth" && (hdr.Typeflag == tar.TypeReg || hdr.Typeflag == tar.TypeRegA) {
			out, err := os.OpenFile(clawAuthDst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
			if err != nil {
				return false, err
			}
			if _, err := io.Copy(out, tr); err != nil {
				_ = out.Close()
				return false, err
			}
			_ = out.Close()
			hasAuth = true
		}
	}

	if !foundClaw {
		return false, fmt.Errorf("`claw` binary was not found inside archive %s", archivePath)
	}
	return hasAuth, nil
}

func atomicReplace(srcPath, dstPath string) error {
	tmpDst := dstPath + ".new." + strconv.Itoa(os.Getpid())
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmpDst, data, 0o755); err != nil {
		return err
	}
	// Preserve existing file ownership if dstPath exists
	if origInfo, statErr := os.Stat(dstPath); statErr == nil {
		if stat, ok := origInfo.Sys().(*syscall.Stat_t); ok {
			_ = os.Chown(tmpDst, int(stat.Uid), int(stat.Gid))
		}
	}
	return os.Rename(tmpDst, dstPath)
}

func checkDirWritable(dir string) error {
	testFile := filepath.Join(dir, fmt.Sprintf(".claw-perm-test-%d", os.Getpid()))
	f, err := os.OpenFile(testFile, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_ = f.Close()
	_ = os.Remove(testFile)
	return nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func isBuildOrTempDir(path string) bool {
	clean := filepath.Clean(path)
	parts := strings.Split(clean, string(filepath.Separator))
	for _, p := range parts {
		if p == "build" || p == "tmp" || p == "temp" || strings.HasPrefix(p, "claw-upgrade-") {
			return true
		}
	}
	return false
}

func confirmPrompt(prompt string) (bool, error) {
	fmt.Printf("%s [y/N]: ", prompt)
	reader := bufio.NewReader(os.Stdin)
	response, err := reader.ReadString('\n')
	if err != nil {
		return false, nil
	}
	response = strings.TrimSpace(strings.ToLower(response))
	return response == "y" || response == "yes", nil
}

// compareSemVer compares two SemVer strings (e.g. "0.5.3" and "0.5.4").
// Returns -1 if v1 < v2, 0 if v1 == v2, 1 if v1 > v2.
func compareSemVer(v1, v2 string) int {
	clean1 := strings.TrimPrefix(strings.Split(v1, "+")[0], "v")
	clean2 := strings.TrimPrefix(strings.Split(v2, "+")[0], "v")

	parts1 := strings.Split(clean1, ".")
	parts2 := strings.Split(clean2, ".")

	maxParts := len(parts1)
	if len(parts2) > maxParts {
		maxParts = len(parts2)
	}

	for i := 0; i < maxParts; i++ {
		var n1, n2 int
		if i < len(parts1) {
			n1, _ = strconv.Atoi(parts1[i])
		}
		if i < len(parts2) {
			n2, _ = strconv.Atoi(parts2[i])
		}
		if n1 < n2 {
			return -1
		}
		if n1 > n2 {
			return 1
		}
	}
	return 0
}

func restartActiveService() {
	if runtime.GOOS == "linux" {
		// Check user service
		if err := exec.Command("systemctl", "--user", "is-active", "--quiet", "claw").Run(); err == nil {
			fmt.Println("Restarting systemd user service claw...")
			if rErr := exec.Command("systemctl", "--user", "restart", "claw").Run(); rErr == nil {
				fmt.Println("User service restarted successfully.")
				return
			}
		}
		// Check system service
		if err := exec.Command("systemctl", "is-active", "--quiet", "claw").Run(); err == nil {
			if os.Geteuid() == 0 {
				fmt.Println("Restarting systemd system service claw...")
				if rErr := exec.Command("systemctl", "restart", "claw").Run(); rErr == nil {
					fmt.Println("System service restarted successfully.")
					return
				}
			} else {
				fmt.Println("Note: System service claw is active. Run `sudo systemctl restart claw` to apply the update.")
			}
		}
	} else if runtime.GOOS == "darwin" {
		label := "com.pivotllm.claweh"
		out, err := exec.Command("launchctl", "list").Output()
		if err == nil && strings.Contains(string(out), label) {
			fmt.Printf("Restarting launchd service %s...\n", label)
			uid := strconv.Itoa(os.Getuid())
			if kErr := exec.Command("launchctl", "kickstart", "-k", "gui/"+uid+"/"+label).Run(); kErr == nil {
				fmt.Println("Launchd service restarted successfully.")
			} else {
				_ = exec.Command("launchctl", "kickstart", "-k", "system/"+label).Run()
			}
		}
	}
}
