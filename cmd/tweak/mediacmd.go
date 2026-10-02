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
  xollama media fetch --dir DIR flux2-klein-4b   keep a local copy, once
  xollama media create flux2-klein-4b --dir DIR  fill the server from it
  xollama media voices mannix/outetts:0.3        a speech model's voices

A template is an ordinary model whose xollama.json carries the media; the
server fetches every component from Hugging Face by its sha256, and push, pull
and rm handle them like weights. With --dir, a component already in that
directory (laid out <owner>/<repo>/<file>) is uploaded from it instead.
Change one afterwards with xollama tweak model.`,
	}
	mediaCmd.AddCommand(mediaListCommand(), mediaSearchCommand(), mediaFilesCommand(), mediaCreateCommand(opts), mediaFetchCommand(), mediaVoicesCommand(opts))
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
	fmt.Fprintln(w, "ID\tKIND\tNAME\tPULL\tNEEDS\tDESCRIPTION")
	n := 0
	for _, e := range mediahub.Catalog() {
		if kind != "" && e.Kind != kind {
			continue
		}
		needs := e.Needs
		if needs == "" {
			needs = "-"
		}
		pull := "-"
		if e.Published {
			pull = e.Template
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", e.ID, e.Kind, e.Name, pull, needs, e.Description)
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

The server fetches every component from Hugging Face by its sha256, except
one already in the --dir mirror (xollama media fetch), which is uploaded.`,
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
			dir, _ := cmd.Flags().GetString("dir")
			return runMediaCreate(cmd.Context(), client, cmd.OutOrStdout(), args, to, dir, dry)
		},
	}
	cmd.Flags().String("to", "", "Add the media to this existing model instead of creating a template")
	cmd.Flags().Bool("dry-run", false, "Resolve and print the config without fetching or writing")
	cmd.Flags().String("dir", "", "A local mirror (xollama media fetch): components found there are uploaded from it")
	return cmd
}

func runMediaCreate(ctx context.Context, client *api.Client, out io.Writer, args []string, to, dir string, dry bool) error {
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
		where := ""
		if p, ok := mediahub.Mirrored(dir, f); ok {
			mediaFiles[f.Digest] = p
			where = "  (local copy)"
		} else {
			mediaSources[f.Digest] = ref.String()
		}
		fmt.Fprintf(out, "   %s  %s  %s%s\n", f.Digest[7:19], format.HumanBytes(f.Size), ref, where)
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

func mediaFetchCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fetch --dir DIR (ID|hf.co/OWNER/REPO/FILE)...",
		Short: "Download catalog entries or Hugging Face files into a local mirror",
		Long: `Download media components into a local directory, laid out
<owner>/<repo>/<file>, so they are downloaded once per machine.

A file already there with the right sha256 is kept; a new one is checked
against the sha256 Hugging Face names before it takes its place. Give catalog
ids (xollama media list), hf.co references, or --all for the whole catalog.
Then xollama media create --dir DIR uploads from the mirror.

This runs on the client and needs no server.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, _ := cmd.Flags().GetString("dir")
			all, _ := cmd.Flags().GetBool("all")
			if dir == "" {
				return errors.New("--dir is required")
			}
			if all {
				for _, e := range mediahub.Catalog() {
					args = append(args, e.ID)
				}
			}
			if len(args) == 0 {
				return errors.New("name catalog ids or hf.co references, or --all")
			}
			return runMediaFetch(cmd.Context(), cmd.OutOrStdout(), dir, args)
		},
	}
	cmd.Flags().String("dir", "", "The mirror directory")
	cmd.Flags().Bool("all", false, "Every file of every catalog entry")
	return cmd
}

// mediaFetch puts one file in the mirror; tests swap it.
var mediaFetch = func(ctx context.Context, dir string, f mediahub.File, progress func(done, total int64)) (string, bool, error) {
	return mediahub.Fetch(ctx, &http.Client{}, dir, f, progress)
}

func runMediaFetch(ctx context.Context, out io.Writer, dir string, args []string) error {
	var refs []string
	for _, a := range args {
		if mediahub.IsRef(a) {
			refs = append(refs, a)
			continue
		}
		e, ok := mediahub.Find(a)
		if !ok {
			return fmt.Errorf("no catalog entry %q; xollama media list shows them", a)
		}
		refs = append(refs, e.Refs()...)
	}
	seen := map[string]bool{}
	for _, s := range refs {
		if seen[s] {
			continue
		}
		seen[s] = true
		ref, err := mediahub.ParseRef(s)
		if err != nil {
			return err
		}
		f, err := hubResolve(ctx, ref)
		if err != nil {
			return err
		}
		next := int64(25)
		p, fetched, err := mediaFetch(ctx, dir, f, func(done, total int64) {
			if total > 0 && done*100/total >= next {
				fmt.Fprintf(out, "   %s %d%% of %s\n", ref.Path, done*100/total, format.HumanBytes(total))
				for next <= done*100/total {
					next += 25
				}
			}
		})
		if err != nil {
			return err
		}
		state := "already there"
		if fetched {
			state = "fetched"
		}
		fmt.Fprintf(out, "   %s  %s  %s  %s\n", f.Digest[7:19], format.HumanBytes(f.Size), p, state)
	}
	return nil
}

func mediaVoicesCommand(opts Options) *cobra.Command {
	return &cobra.Command{
		Use:   "voices MODEL",
		Short: "List a speech model's voices",
		Long: `List the voices a speech model answers to: the engine's own, and the names
its template maps to them (OpenAI's alloy, nova, ...). The default voice is
marked. Starts the model's speech engine when it is not running.`,
		Args: cobra.ExactArgs(1),
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
			resp, err := client.MediaVoices(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			printVoices(cmd.OutOrStdout(), resp)
			return nil
		},
	}
}

func printVoices(out io.Writer, r *api.MediaVoicesResponse) {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "VOICE\tALSO ANSWERS TO\tDEFAULT")
	for _, v := range r.Voices {
		def := ""
		if v.ID == r.Default {
			def = "*"
		}
		also := strings.Join(v.Aliases, ", ")
		if also == "" {
			also = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", v.ID, also, def)
	}
	w.Flush()
	if len(r.ResponseFormats) > 0 {
		fmt.Fprintf(out, "\nformats: %s", strings.Join(r.ResponseFormats, ", "))
		if r.SampleRate > 0 {
			fmt.Fprintf(out, " at %d Hz", r.SampleRate)
		}
		fmt.Fprintln(out)
	}
}
