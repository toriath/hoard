// Keyboard control of a seek bar (the audio waveform), following the ARIA
// slider pattern: arrows step, Page keys jump, Home and End go to the ends.

/** Seconds one arrow press moves: fine for short sounds, coarse for tracks. */
export function seekStep(duration: number): number {
  return duration <= 20 ? 0.25 : 5
}

/**
 * The position a key press leads to, or null when the key is not one the bar
 * handles (so the browser keeps its default for it).
 */
export function seekTarget(key: string, time: number, duration: number): number | null {
  if (!(duration > 0)) return null
  const step = seekStep(duration)
  const page = Math.max(1, duration / 10)
  let next: number
  switch (key) {
    case 'ArrowRight':
    case 'ArrowUp':
      next = time + step
      break
    case 'ArrowLeft':
    case 'ArrowDown':
      next = time - step
      break
    case 'PageUp':
      next = time + page
      break
    case 'PageDown':
      next = time - page
      break
    case 'Home':
      next = 0
      break
    case 'End':
      next = duration
      break
    default:
      return null
  }
  return Math.min(duration, Math.max(0, next))
}
