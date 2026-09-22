# Large images: local compression and experimental uploads

**Settings → Large images** controls what Kilo Proxy does when inline images make a Responses request too large for the Gateway. Choose one mode:

| Mode | Behavior |
| --- | --- |
| **Off** (default) | Send image data unchanged. Oversized requests can still fail with HTTP 413. |
| **Compress locally** | Optimize the copies sent with the request, using your selected quality profile. |
| **Upload to Kilo · Experimental** | Preserve the image bytes and substitute temporary Kilo links. |

The selected mode and compression profile save automatically in `settings.json` as `imageTransport.mode` and `imageTransport.profile`. They apply to new requests without restarting. The optional browser helper exposes the same choices under **Large images**. Switching modes never cancels cleanup for earlier uploads. Smaller requests and local original files are unchanged in every mode.

## Local compression

Compression starts only when a Responses request exceeds **4,400,000 bytes**, with a margin below Kilo's 4.5 MB limit. The proxy first tries lossless optimization. If the request is still too large, it uses the selected fixed profile:

| Profile | Maximum longest side | JPEG quality |
| --- | --- | --- |
| **High quality** (default profile) | 3072 px | 92 |
| **Balanced** | 2048 px | 85 |
| **Small size** | 1280 px | 75 |

Aspect ratio is preserved. These are encoder settings, not a guarantee of a specific perceptual quality or final byte size. Static PNG images are first optimized without changing their pixels. Opaque static PNG, JPEG, and WebP images can use JPEG compression with the selected profile. Images with transparency remain PNG instead of being flattened. GIF images and animated PNG/WebP images pass through unchanged so animation frames are not discarded. Only outbound image copies are changed; generated originals, attachments on disk, and the client's saved conversation are not rewritten.

JPEG orientation metadata is retained. PNG color and orientation metadata are preserved during lossless optimization. PNG and WebP images with EXIF metadata are excluded from lossy conversion to avoid changing their orientation.

The proxy does not step down to a lower profile if your selection still does not fit. It returns an explanation instead. It never falls back from local compression to remote uploads automatically. Change the profile yourself, choose experimental uploads explicitly, or compact the conversation if needed.

## Experimental uploads

This feature uses Kilo's own Cloud Agent attachment storage with your configured Kilo account. **Reusing that storage for Gateway requests is not documented as a supported Gateway integration.** It may change or stop working. No ngrok process, storage account, bucket configuration, or extra executable is needed.

### How uploads work

For an authenticated **Responses** request larger than **4,400,000 bytes**, the proxy checks whether moving inline images to temporary links can bring the body below that budget. The margin leaves room below the upstream 4.5 MB limit. Only as many of the largest images as necessary are uploaded; smaller requests retain their existing behavior.

1. The proxy validates the image data and checks whether the request can fit before uploading anything.
2. It uploads the original bytes to Kilo and substitutes temporary image links in the outbound request.
3. The model provider downloads the originals from Kilo. Dimensions, quality, transparency, and the encoded image bytes remain unchanged.
4. After the response finishes or the request is canceled or fails, the proxy requests deletion of the attachments.

This does not change your local files or rewrite the client's saved conversation. A later request containing the same inline images may need another upload. Existing MCP previews remain previews: this feature preserves the bytes received from the client and does not recover a full-resolution original from a smaller preview.

### Upload limits

- PNG, JPEG, GIF, and WebP inline image parts in Responses messages and image parts returned in function-tool outputs are supported.
- Up to **five unique images** are uploaded for one request, with a maximum of **20 MiB per image**. Identical images in the same request share one upload. Additional images can remain inline if the final body fits the budget.
- The existing **32 MiB local request-body limit** still applies. This feature cannot receive arbitrarily large conversations.
- Requests that use uploaded images have a **10-minute total timeout**, shorter than the temporary links' 15-minute lifetime.
- Long text, arbitrary base64 strings in tool arguments or text, remote image URLs, and other protocols are not transformed. The feature does not remove conversation context or silently recompress images to make a request fit.
- Provider image, resolution, context, and account restrictions still apply. Moving images to links does not reduce the model's image-token usage or guarantee a lower inference charge.

If the eligible images cannot make the body fit, the request fails with an explanation before uploading. If upload or inference fails, the proxy attempts to delete any attachments it already created. It does not automatically retry paid inference.

### Deletion and privacy

The images leave your computer and are stored temporarily in your Kilo account. Temporary links grant access to anyone who obtains them until they expire or deletion takes effect. For requests that use uploaded images, captured response bodies are omitted because a model can repeat temporary links across streaming chunks. Request metadata and the redacted outbound request remain available when capture is enabled. Keep captures private; arbitrary prompt secrets are not automatically detected.

Deletion runs after the response body finishes, including streaming responses, rather than when the initial response headers arrive. Cancellation also triggers cleanup. Cleanup uses a separate bounded context so that canceling the inference request does not immediately cancel the deletion attempt. Failed deletions are retried within a 30-second cleanup window, with up to three attempts.

If cleanup cannot be confirmed, **Settings → Large images** displays **Image cleanup needs attention**. Selecting **Off** or **Compress locally** prevents uploads for new requests; it does not stop cleanup already in progress or erase the warning.

**Link expiry does not mean file deletion.** Normal quit waits for the bounded cleanup attempt before exiting. The feature keeps its upload bookkeeping and cleanup warning in memory only. Network failures, force quitting, a crash, or a machine shutdown can leave remote copies behind. Restarting the app does not recover a deletion queue or prove that earlier files were removed. There is no guarantee of deletion after an abrupt exit, and the proxy does not claim a storage-retention guarantee from Kilo.

Request capture remains a separate opt-in preference. Enabling experimental uploads does not enable debug capture or save image bodies to a new local history file.

## Validation and implementation source

Automated tests use synthetic storage and Gateway services. They exercise settings persistence, opt-in behavior, request transformation, unchanged image bytes, and cleanup paths without uploading personal images or making paid inference requests. The browser settings checks use the real Go backend in isolated temporary profiles.

An additional real Gateway test is skipped unless explicitly enabled. It creates four synthetic images, verifies byte-for-byte downloads, sends one bounded inference, and verifies deletion through the production Go request pipeline. It reads a chosen saved profile without changing it or restarting the running app. This test uses account credits and is not enabled in CI:

```sh
KILO_IMAGE_UPLOAD_LIVE_TEST=1 \
KILO_IMAGE_UPLOAD_CONFIG_DIR="$HOME/Library/Application Support/kilo-proxy" \
go test -run '^TestImageUploadsLiveGateway$' -count=1 -v .
```

The experimental storage behavior is based on Kilo's [Cloud Agent pending attachments implementation](https://github.com/Kilo-Org/cloud/blob/main/apps/web/src/lib/r2/cloud-agent-pending-uploads.ts). A successful integration test establishes current behavior, not a stable public API contract. See also [payload limits and recovery](codex-images.md#payload-limits-and-413-errors).
