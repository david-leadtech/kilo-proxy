# Kilo Proxy 0.67.6

Includes PR #76 by hcandon: requests rejected because the gateway cannot download a remote image can continue with an explicit text marker in place of the remote images.

The fallback runs once, only for the known structured HTTP 400 download error, across Responses, Chat Completions, and Anthropic Messages. It preserves text, tool arguments, inline images and file attachments. All eligible HTTP(S) image parts in that request are omitted because the error does not identify an individual failed image.

Additional regression fixes preserve cancellation and response read errors, close inspected bodies once, and keep final token and cost accounting. Successful streams and unrelated provider errors retain their normal behavior. The direct ChatGPT subscription route remains unchanged.

The release retains the current APT publisher and uses the normal version-tag publication route under the existing GitHub Pages protection.
