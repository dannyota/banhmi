// Command rerankkernel drives the offline reranker-scoring experiment on
// Kaggle (see PLAN.md "Reranker experiment"): upload the candidate dumps as a
// dataset version, wait until it is processed, push the scoring kernel pinned
// to a T4 (the free CLI cannot select an accelerator and a P100 assignment
// breaks the preinstalled torch), poll to completion, and download the
// *-scores.jsonl outputs. Auth comes from KAGGLE_API_TOKEN.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	kaggle "danny.vn/kaggle"
	"danny.vn/kaggle/datasets"
	"danny.vn/kaggle/kernels"
)

func main() {
	var (
		dumpsDir = flag.String("dumps", "", "directory holding *-candidates.jsonl")
		script   = flag.String("script", "", "path to the python scoring script")
		outDir   = flag.String("out", "", "directory to download *-scores.jsonl into")
		slug     = flag.String("slug", "banhmi-rerank", "dataset/kernel slug prefix")
		timeout  = flag.Duration("timeout", 40*time.Minute, "kernel run timeout")
		cleanup  = flag.Bool("cleanup", false, "delete the experiment's Kaggle kernel and dataset, then exit")
	)
	flag.Parse()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if *cleanup {
		if err := runCleanup(*slug, log); err != nil {
			log.Error("rerankkernel cleanup", "err", err)
			os.Exit(1)
		}
		return
	}
	if err := run(*dumpsDir, *script, *outDir, *slug, *timeout, log); err != nil {
		log.Error("rerankkernel", "err", err)
		os.Exit(1)
	}
}

// runCleanup removes the experiment kernel and dataset from Kaggle — the same
// leave-nothing-behind etiquette as the bulk-embed pipeline.
func runCleanup(slug string, log *slog.Logger) error {
	ctx := context.Background()
	client, err := kaggle.New()
	if err != nil {
		return fmt.Errorf("kaggle client: %w", err)
	}
	owner, err := client.WhoAmI(ctx)
	if err != nil {
		return fmt.Errorf("resolve kaggle owner: %w", err)
	}
	if err := kernels.New(client).DeleteKernel(ctx, owner, slug+"-score"); err != nil {
		log.Warn("delete kernel", "err", err)
	} else {
		log.Info("deleted kernel", "slug", slug+"-score")
	}
	if err := datasets.New(client).DeleteDataset(ctx, owner, slug+"-cands"); err != nil {
		log.Warn("delete dataset", "err", err)
	} else {
		log.Info("deleted dataset", "slug", slug+"-cands")
	}
	return nil
}

func run(dumpsDir, script, outDir, slug string, timeout time.Duration, log *slog.Logger) error {
	if dumpsDir == "" || script == "" || outDir == "" {
		return fmt.Errorf("-dumps, -script, and -out are required")
	}
	dumps, err := filepath.Glob(filepath.Join(dumpsDir, "*-candidates.jsonl"))
	if err != nil {
		return fmt.Errorf("glob candidate dumps: %w", err)
	}
	if len(dumps) == 0 {
		return fmt.Errorf("no *-candidates.jsonl under %s", dumpsDir)
	}
	text, err := os.ReadFile(script)
	if err != nil {
		return fmt.Errorf("read script: %w", err)
	}

	ctx := context.Background()
	client, err := kaggle.New()
	if err != nil {
		return fmt.Errorf("kaggle client: %w", err)
	}
	owner, err := client.WhoAmI(ctx)
	if err != nil {
		return fmt.Errorf("resolve kaggle owner: %w", err)
	}
	ds := datasets.New(client)
	ks := kernels.New(client)
	dataSlug := slug + "-cands"
	kernelSlug := slug + "-score"

	// Version the dataset (created on first use), then wait until processed —
	// a kernel pushed against a still-processing version mounts an empty input.
	newDataset := false
	if _, err := ds.Status(ctx, owner, dataSlug); err != nil {
		newDataset = true
	}
	if err := ds.CreateOrVersion(ctx, owner, dataSlug, dataSlug, dumps, newDataset, "candidates "+time.Now().UTC().Format(time.RFC3339)); err != nil {
		return fmt.Errorf("dataset upload: %w", err)
	}
	for i := 0; ; i++ {
		st, err := ds.Status(ctx, owner, dataSlug)
		if err == nil && strings.EqualFold(st, "ready") {
			break
		}
		if i > 30 {
			return fmt.Errorf("dataset not ready after %d polls (last status %q, err %v)", i, st, err)
		}
		log.Info("waiting for dataset processing", "status", st)
		time.Sleep(10 * time.Second)
	}
	log.Info("dataset ready", "dataset", owner+"/"+dataSlug)

	resp, err := ks.Push(ctx, &kernels.ApiSaveKernelRequest{
		Slug:               fmt.Sprintf("%s/%s", owner, kernelSlug),
		NewTitle:           kernelSlug,
		Text:               string(text),
		Language:           "python",
		KernelType:         "script",
		IsPrivate:          true,
		EnableGpu:          true,
		EnableInternet:     true,
		MachineShape:       "NvidiaTeslaT4",
		DatasetDataSources: []string{owner + "/" + dataSlug},
	})
	if err != nil {
		return fmt.Errorf("push kernel: %w", err)
	}
	if resp.Error != "" {
		return fmt.Errorf("push kernel rejected: %s", resp.Error)
	}
	log.Info("kernel pushed", "slug", kernelSlug, "version", resp.VersionNumber)

	deadline := time.Now().Add(timeout)
	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("kernel timed out after %s", timeout)
		}
		st, err := ks.Status(ctx, owner, kernelSlug)
		if err != nil {
			log.Warn("status poll failed; retrying", "err", err)
			time.Sleep(30 * time.Second)
			continue
		}
		s := strings.ToUpper(string(st.Status))
		switch {
		case strings.Contains(s, "COMPLETE"):
			log.Info("kernel complete")
		case strings.Contains(s, "ERROR"), strings.Contains(s, "CANCEL"):
			return fmt.Errorf("kernel failed: status %s: %s", st.Status, st.FailureMessage)
		default:
			log.Info("kernel running", "status", st.Status)
			time.Sleep(30 * time.Second)
			continue
		}
		break
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	files, err := ks.Output(ctx, owner, kernelSlug, outDir)
	if err != nil {
		return fmt.Errorf("download kernel output: %w", err)
	}
	scored := 0
	for _, f := range files {
		if strings.HasSuffix(f, "-scores.jsonl") {
			scored++
		}
		log.Info("output", "file", f)
	}
	if scored == 0 {
		return fmt.Errorf("kernel produced no *-scores.jsonl (see downloaded log)")
	}
	log.Info("scores downloaded", "files", scored, "dir", outDir)
	return nil
}
