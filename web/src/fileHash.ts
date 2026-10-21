import { sha256 } from '@noble/hashes/sha2.js'

// Resume verification must also work on an explicitly selected HTTP intranet.
export async function fileSHA256(blob: Blob): Promise<string> {
  const data = await blob.arrayBuffer()
  const native = globalThis.crypto?.subtle
  const hash = native
    ? new Uint8Array(await native.digest('SHA-256', data))
    : sha256(new Uint8Array(data))
  return Array.from(hash, b => b.toString(16).padStart(2, '0')).join('')
}
