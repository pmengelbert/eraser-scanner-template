package main

import (
	"context"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/containers/podman/v2/pkg/ctime"
	"github.com/eraser-dev/eraser/api/unversioned"
	"github.com/eraser-dev/eraser/pkg/logger"
	template "github.com/eraser-dev/eraser/pkg/scanners/template"
	"k8s.io/apimachinery/pkg/util/yaml"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	containerdDataDir = "/var/lib/containerd/io.containerd.content.v1.content"
)

var (
	config        = flag.String("config", "", "path to the configuration file")
	enableProfile = flag.Bool("enable-pprof", false, "enable pprof profiling")
	profilePort   = flag.Int("pprof-port", 6060, "port for pprof profiling. defaulted to 6060 if unspecified")

	log = logf.Log.WithName("scanner").WithValues("provider", "customScanner")

	maxAge time.Duration = 7 * 24 * time.Hour
)

type Config struct {
	// MaxAge is the oldest an image may be without being removed
	MaxAge string `json:"maxAge" yaml:"maxAge"`
}

func main() {
	if err := logger.Configure(); err != nil {
		fmt.Fprintf(os.Stderr, "error setting up logger: %s", err)
		os.Exit(1)
	}
	if config == nil || *config == "" {
		s := "/config/controller_manager_config.yaml"
		config = &s
	}

	c, err := loadConfig(*config)
	if err != nil {
		log.Error(err, "unable to read configuration file")
		os.Exit(1)
	}

	if c.MaxAge != "" {
		var err error
		maxAge, err = time.ParseDuration(c.MaxAge)
		if err != nil {
			log.Error(err, "unable to parse duration", "config.MaxAge", c.MaxAge)
			os.Exit(1)
		}
	}

	// create image provider with custom values
	imageProvider := template.NewImageProvider(
		template.WithContext(context.Background()),
		template.WithMetrics(true),
		template.WithDeleteScanFailedImages(true),
		template.WithLogger(log),
	)

	// retrieve list of all non-running, non-excluded images from collector container
	allImages, err := imageProvider.ReceiveImages()
	if err != nil {
		log.Error(err, "unable to retrieve list of images from collector container")
		os.Exit(1)
	}

	// scan images with custom scanner
	nonCompliant, failedImages := scan(allImages)

	// send images to eraser container
	if err := imageProvider.SendImages(nonCompliant, failedImages); err != nil {
		log.Error(err, "unable to send non-compliant images to eraser container")
		os.Exit(1)
	}

	// complete scan
	if err := imageProvider.Finish(); err != nil {
		log.Error(err, "unable to complete scanner")
		os.Exit(1)
	}
}

// TODO: implement customized scanner
func scan(allImages []unversioned.Image) ([]unversioned.Image, []unversioned.Image) {
	// scan images and partition into non-compliant and failedImages
	var nonCompliant, failedImages []unversioned.Image

	// Create a set of the images, for use during the filesystem walk
	digests := make(map[string]unversioned.Image, len(allImages))
	for _, img := range allImages {
		for _, dgst := range img.Digests {
			key := strings.TrimPrefix(dgst, "sha256:")
			digests[key] = img
		}
	}

	ctrFs := os.DirFS(containerdDataDir)
	if err := fs.WalkDir(ctrFs, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() {
			return nil
		}

		img, shouldScan := digests[d.Name()]
		if !shouldScan {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			failedImages = append(failedImages, img)
			return nil
		}

		created := ctime.Created(info)
		log.Info("image scanned", "image", img, "created_at", created.String(), "image age", time.Since(created).String())
		if time.Since(created) > maxAge {
			nonCompliant = append(nonCompliant, img)
		}

		return nil
	}); err != nil {
		log.Error(err, "all images considered failed")
		return []unversioned.Image{}, allImages
	}

	log.Info("images", "nonCompliant", nonCompliant, "failed", failedImages)

	return nonCompliant, failedImages
}

func loadConfig(filename string) (Config, error) {
	cfg := Config{MaxAge: "168h"}

	b, err := os.ReadFile(filename)
	if err != nil {
		log.Error(err, "unable to read eraser config", "config", string(b))
		return cfg, err
	}

	log.Info("eraserConfig", "config", string(b))
	var eraserConfig unversioned.EraserConfig
	err = yaml.Unmarshal(b, &eraserConfig)
	if err != nil {
		log.Error(err, "unable to unmarshal eraser config", "config", string(b))
	}

	scanCfgYaml := eraserConfig.Components.Scanner.Config
	log.Info("scanner config yaml string", "config", string(*scanCfgYaml))
	scanCfgBytes := []byte("")
	if scanCfgYaml != nil {
		scanCfgBytes = []byte(*scanCfgYaml)
	}

	log.Info("scanner config", "config", string(scanCfgBytes))
	err = yaml.Unmarshal(scanCfgBytes, &cfg)
	if err != nil {
		log.Error(err, "unable to unmarshal scanner config", "config", string(scanCfgBytes))
		return cfg, err
	}

	return cfg, nil
}
