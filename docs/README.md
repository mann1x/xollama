# Documentation

<!-- xollama-hook: docs-entry -- xollama's own pages first; upstream's list follows unchanged. -->
### xollama

This is xollama, a soft fork of Ollama. It listens on **22434** (set with
`XOLLAMA_HOST`, never `OLLAMA_HOST`). Start with the
[repository README](../README.md), then:

* [What xollama adds](./xollama/index.mdx)
* [Running in Docker](./xollama/docker.mdx) — the `mannixita/xollama` image, GPU passthrough, volumes, environment variables
* [Choosing an engine](./xollama/engines.mdx) · [Tuning opencoti](./xollama/opencoti-tuning.mdx) · [Passing your own engine flags](./xollama/engine-args.mdx)
* [KV cache types](./xollama/kv-cache.mdx) · [Concurrent requests (slots)](./xollama/slots.mdx) · [Sessions and prefix pools](./xollama/sessions.mdx) · [Beyond the trained context (DCA)](./xollama/dca.mdx)
* [Settings that belong to the model](./xollama/model-settings.mdx) · [Changing a model's settings (`xollama tweak`)](./xollama/tweak.mdx)
* [Council chat](./xollama/council.mdx) · [Bounding thinking](./xollama/think-budget.mdx) · [Gemma-4 drafters](./xollama/gemma4-drafter.mdx)
* [The default port](./xollama/default-port.mdx) · [The local API key](./xollama/api-key.mdx) · [Tokenizer endpoints](./xollama/tokenize.mdx) · [Seeing what the engine did](./xollama/introspection.mdx)

The rest of this page is upstream Ollama's documentation, which applies to
xollama except where the pages above say otherwise.

### Getting Started
* [Quickstart](https://docs.ollama.com/quickstart)
* [Examples](./examples.md)
* [Importing models](https://docs.ollama.com/import)
* [MacOS Documentation](https://docs.ollama.com/macos)
* [Linux Documentation](https://docs.ollama.com/linux)
* [Windows Documentation](https://docs.ollama.com/windows)
* [Docker Documentation](https://docs.ollama.com/docker)

### Reference

* [API Reference](https://docs.ollama.com/api)
* [Modelfile Reference](https://docs.ollama.com/modelfile)
* [OpenAI Compatibility](https://docs.ollama.com/api/openai-compatibility)
* [Anthropic Compatibility](./api/anthropic-compatibility.mdx)

### Resources

* [Troubleshooting Guide](https://docs.ollama.com/troubleshooting)
* [FAQ](https://docs.ollama.com/faq)
* [Development guide](./development.md)
