import assert from 'node:assert/strict'
import { createHash, webcrypto } from 'node:crypto'
import test from 'node:test'
import { fileSHA256 } from '../src/fileHash.ts'

for (const [name, cryptoValue] of [['HTTPS Web Crypto', webcrypto], ['HTTP without subtle', {}], ['HTTP without crypto', undefined]]) {
  test(name, async () => {
    const previous = Object.getOwnPropertyDescriptor(globalThis, 'crypto')
    Object.defineProperty(globalThis, 'crypto', { value: cryptoValue, configurable: true })
    try {
      const chunk = Uint8Array.from({ length: 5 * 1024 * 1024 + 17 }, (_, i) => i % 251)
      for (const blob of [new Blob([]), new Blob(['abc']), new Blob(['中文文件与续传备注']), new Blob([chunk])]) {
        const expected = createHash('sha256').update(new Uint8Array(await blob.arrayBuffer())).digest('hex')
        assert.equal(await fileSHA256(blob), expected)
      }
    } finally {
      if (previous) Object.defineProperty(globalThis, 'crypto', previous)
      else delete globalThis.crypto
    }
  })
}
