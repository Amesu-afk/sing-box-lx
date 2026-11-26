package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sagernet/sing-box/log"
)

const cronetAndroidVersion = "v0.0.0-20261103043047-3aac4ed44c5c"

type cronetAndroidModule struct {
	architecture string
	localDir     string
}

// withCronetLibraries stages the pinned static archives next to the local cgo
// compatibility modules. The archives are ignored by Git and removed after the
// Android bind, so the repository stays small while CI remains reproducible.
func withCronetLibraries(bindTarget string) (func(), error) {
	modules := []cronetAndroidModule{
		{architecture: "arm", localDir: "third_party/cronet-go-android/arm"},
		{architecture: "arm64", localDir: "third_party/cronet-go-android/arm64"},
		{architecture: "386", localDir: "third_party/cronet-go-android/386"},
		{architecture: "amd64", localDir: "third_party/cronet-go-android/amd64"},
	}
	if bindTarget != "android" {
		architecture := strings.TrimPrefix(bindTarget, "android/")
		for _, module := range modules {
			if module.architecture == architecture {
				modules = []cronetAndroidModule{module}
				break
			}
		}
	}
	var staged []string
	cleanup := func() {
		for _, path := range staged {
			_ = os.Remove(path)
		}
	}
	for _, module := range modules {
		modulePath := "github.com/sagernet/cronet-go/lib/android_" + module.architecture
		command := exec.Command("go", "mod", "download", "-json", modulePath+"@"+cronetAndroidVersion)
		output, err := command.Output()
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("download %s: %w", modulePath, err)
		}
		var downloaded struct {
			Dir string `json:"Dir"`
		}
		if err = json.Unmarshal(output, &downloaded); err != nil {
			cleanup()
			return nil, fmt.Errorf("decode %s download: %w", modulePath, err)
		}
		if downloaded.Dir == "" {
			cleanup()
			return nil, fmt.Errorf("download %s: empty module directory", modulePath)
		}
		sourcePath := filepath.Join(downloaded.Dir, "libcronet.a")
		destinationPath := filepath.Join(module.localDir, "libcronet.a")
		if err = copyCronetLibrary(sourcePath, destinationPath); err != nil {
			cleanup()
			return nil, err
		}
		staged = append(staged, destinationPath)
		log.Info("staged ", destinationPath)
	}
	return cleanup, nil
}

func copyCronetLibrary(sourcePath string, destinationPath string) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("open %s: %w", sourcePath, err)
	}
	defer source.Close()
	if err = os.MkdirAll(filepath.Dir(destinationPath), 0o755); err != nil {
		return fmt.Errorf("create Cronet module directory: %w", err)
	}
	destination, err := os.Create(destinationPath)
	if err != nil {
		return fmt.Errorf("create %s: %w", destinationPath, err)
	}
	_, copyErr := io.Copy(destination, source)
	closeErr := destination.Close()
	if copyErr != nil {
		_ = os.Remove(destinationPath)
		return fmt.Errorf("copy %s: %w", sourcePath, copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(destinationPath)
		return fmt.Errorf("close %s: %w", destinationPath, closeErr)
	}
	return nil
}
