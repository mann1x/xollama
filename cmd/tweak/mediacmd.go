package tweak

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/format"
	"github.com/ollama/ollama/internal/mediahub"
	"github.com/ollama/ollama/types/model"
	"github.com/ollama/ollama/types/xollama"
)

// MediaCommand is `xollama media`: the catalog of media templates, discovery
// on Hugging Face, and building a template from either
// (plans/media-integration.md).
func MediaCommand(opts Options) *cobra.Command {
	mediaCmd := &cobra.Command{
		Use:   "media",
		Short: "Find image, speech, transcription and video models, and make templates of them",
		Long: `Find media models and make xollama templates of them.

  xollama media list                      the curated catalog
  xollama media search tts kokoro         Hugging Face, by task
  xollama media files leejet/FLUX.2-klein-4B-GGUF
  xollama media create flux2-klein-4b     a template named after the entry
  xollama media create kokoro-82m --to qwen3:8b

A template is an ordinary model whose xollama.json carries the media; the
server fetches every component from Hugging Face by its sha256, and push, pull
and rm handle them like weights. Change one afterwards with xollama tweak model.`,
	}
	mediaCmd.AddCommand(mediaListCommand(), mediaSearchCommand(), mediaFilesCommand(), mediaCreateCommand(opts))
	return mediaCmd
}

func hubClient() *http.Client { return &http.Client{Timeout: 60 * time.Second} }

func mediaListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the curated media templates",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			kind, _ := cmd.Flags().GetString("kind")
			return printCatalog(cmd.OutOrStdout(), kind)
		},
	}
	cmd.Flags().String("kind", "", "Only this kind: image, stt, tts, video or mix")
	return cmd
}

func printCatalog(out io.Writer, kind string) error {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tKIND\tNAME\tLICENSE\tNEEDS\tDESCRIPTION")
	n := 0
	for _, e := range mediahub.Catalog() {
		if kind != "" && e.Kind != kind {
			continue
		}
		needs := e.Needs
		if needs == "" {
			needs = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", e.ID, e.Kind, e.Name, e.License, needs, e.Description)
		n++
	}
	if n == 0 {
		return fmt.Errorf("no catalog entry of kind %q", kind)
	}
	return w.Flush()
}

func mediaSearchCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "search KIND [QUERY]",
		Short: "Search Hugging Face for media models of a kind",
		Long: `Search Hugging Face for media models, most downloaded first.

KIND is image, edit, stt, tts or video. Only GGUF repos are listed unless
--all-formats is given; list a repo's files with xollama media files.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			query := ""
			if len(args) == 2 {
				query = args[1]
			}
			all, _ := cmd.Flags().GetBool("all-formats")
			limit, _ := cmd.Flags().GetInt("limit")
			repos, err := mediahub.Search(cmd.Context(), hubClient(), mediahub.Kind(args[0]), query, !all, limit)
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "REPO\tDOWNLOADS\tLIKES")
			for _, r := range repos {
				fmt.Fprintf(w, "%s\t%d\t%d\n", r.ID, r.Downloads, r.Likes)
			}
			return w.Flush()
		},
	}
	cmd.Flags().Bool("all-formats", false, "Include repos that are not GGUF")
	cmd.Flags().Int("limit", 20, "How many repos to list")
	return cmd
}

func mediaFilesCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "files REPO",
		Short: "List a Hugging Face repo's weight files, as references tweak takes",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo := strings.TrimPrefix(strings.TrimPrefix(args[0], "hf.co/"), "huggingface.co/")
			rev, _ := cmd.Flags().GetString("rev")
			files, err := mediahub.Files(cmd.Context(), hubClient(), repo, rev)
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "FORMAT\tSIZE\tREFERENCE")
			for _, f := range files {
				ref := f.Ref(repo, rev).String()
				if f.Digest == "" {
					ref += "  (kept in git: cannot be fetched by digest)"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\n", f.Format, format.HumanBytes(f.Size), ref)
			}
			return w.Flush()
		},
	}
	cmd.Flags().String("rev", "main", "Branch, tag or commit")
	return cmd
}

func mediaCreateCommand(opts Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create ID [NAME]",
		Short: "Make a media template from the catalog, or add one to a model",
		Long: `Make a media template from a catalog entry (xollama media list).

With NAME the template is created under it, otherwise under the entry's id.
With --to MODEL the entry's media is added to that model instead, replacing
any media of the same kind and leaving the rest of the model alone.

The server fetches every component from Hugging Face by its sha256.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.Heartbeat != nil {
				if err := opts.Heartbeat(cmd, args); err != nil {
					return err
				}
			}
			client, err := api.ClientFromEnvironment()
			if err != nil {
				return err
			}
			to, _ := cmd.Flags().GetString("to")
			dry, _ := cmd.Flags().GetBool("dry-run")
			return runMediaCreate(cmd.Context(), client, cmd.OutOrStdout(), args, to, dry)
		},
	}
	cmd.Flags().String("to", "", "Add the media to this existing model instead of creating a template")
	cmd.Flags().Bool("dry-run", false, "Resolve and print the config without fetching or writing")
	return cmd
}

func runMediaCreate(ctx context.Context, client *api.Client, out io.Writer, args []string, to string, dry bool) error {
	entry, ok := mediahub.Find(args[0])
	if !ok {
		return fmt.Errorf("no catalog entry %q; xollama media list shows them", args[0])
	}
	if to != "" && len(args) == 2 {
		return errors.New("give either NAME or --to, not both")
	}
	name := entry.ID
	if len(args) == 2 {
		name = args[1]
	}
	if to != "" {
		name = to
	}
	if !model.ParseName(name).IsValid() {
		return fmt.Errorf("invalid model name: %s", name)
	}
	if entry.Needs != "" {
		fmt.Fprintf(out, "%s needs %s; it can be created now and served once the engine has it.\n", entry.ID, entry.Needs)
	}

	// Name every component by its digest on the hub.
	media := entry.Media.Clone()
	digests := map[string]string{}
	for _, s := range entry.Refs() {
		ref, err := mediahub.ParseRef(s)
		if err != nil {
			return err
		}
		f, err := hubResolve(ctx, ref)
		if err != nil {
			return err
		}
		digests[s] = f.Digest
		mediaSources[f.Digest] = ref.String()
		fmt.Fprintf(out, "   %s  %s  %s\n", f.Digest[7:19], format.HumanBytes(f.Size), ref)
	}
	media.MapComponents(func(s string) string { return digests[s] })

	cfg := &xollama.Config{Media: media}
	from := ""
	if to != "" {
		current, err := showConfig(ctx, client, to)
		if err != nil {
			return err
		}
		cfg = current
		if cfg.Media == nil {
			cfg.Media = &xollama.Media{}
		}
		if media.Image != nil {
			cfg.Media.Image = media.Image
		}
		if media.STT != nil {
			cfg.Media.STT = media.STT
		}
		if media.TTS != nil {
			cfg.Media.TTS = media.TTS
		}
		if media.Video != nil {
			cfg.Media.Video = media.Video
		}
		from = to
	}
	data, err := cfg.Marshal()
	if err != nil {
		return err
	}
	if dry {
		fmt.Fprintf(out, "\n%s\n\n--dry-run: nothing fetched, %s not written.\n", data, name)
		return nil
	}
	stamped, err := xollama.Parse(data)
	if err != nil {
		return err
	}
	if err := uploadMedia(ctx, client, stamped, out); err != nil {
		return err
	}
	req := &api.CreateRequest{Model: name, From: from, Xollama: stamped}
	if err := client.Create(ctx, req, func(p api.ProgressResponse) error {
		if p.Status == "success" {
			fmt.Fprintf(out, "   success\n")
		}
		return nil
	}); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s carries %s. `xollama show %s` lists it.\n", name, strings.Join(stamped.Media.Kinds(), ", "), name)
	return nil
}
