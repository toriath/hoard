import { useSyncExternalStore } from 'react'

// One shared <audio> element so that only a single preview plays at a time.

let el: HTMLAudioElement | undefined
let current = ''
const subs = new Set<() => void>()
const emit = () => subs.forEach((s) => s())

let switching = false

export function togglePreview(key: string, url: string) {
  el ??= Object.assign(new Audio(), {
    onended: () => {
      current = ''
      emit()
    },
    onpause: () => {
      if (!switching) {
        current = ''
        emit()
      }
    },
  })
  if (current === key) {
    el.pause()
    return
  }
  switching = true
  el.src = url
  current = key
  emit()
  el.play()
    .catch(() => {
      // Only if this is still the preview the user last asked for: a quick
      // second click aborts the first play(), which must not reset the second.
      if (current === key) {
        current = ''
        emit()
      }
    })
    .finally(() => {
      if (current === key || current === '') switching = false
    })
}

export function stopPreview() {
  el?.pause()
}

export function useNowPlaying(): string {
  return useSyncExternalStore(
    (cb) => {
      subs.add(cb)
      return () => subs.delete(cb)
    },
    () => current,
  )
}
