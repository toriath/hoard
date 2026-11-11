// 3D preview: loads a model with three.js, frames it and renders it.
// Imported lazily so the (large) three.js bundle only loads when needed.

import * as THREE from 'three'
import { OrbitControls } from 'three/examples/jsm/controls/OrbitControls.js'
import { RoomEnvironment } from 'three/examples/jsm/environments/RoomEnvironment.js'
import { FBXLoader } from 'three/examples/jsm/loaders/FBXLoader.js'
import { GLTFLoader } from 'three/examples/jsm/loaders/GLTFLoader.js'
import { OBJLoader } from 'three/examples/jsm/loaders/OBJLoader.js'
import { PLYLoader } from 'three/examples/jsm/loaders/PLYLoader.js'
import { STLLoader } from 'three/examples/jsm/loaders/STLLoader.js'

export interface LoadedModel {
  object: THREE.Object3D
  animations: THREE.AnimationClip[]
  triangles: number
}

const defaultMaterial = () => new THREE.MeshStandardMaterial({ color: 0xb9b2a3, roughness: 0.75, metalness: 0.05 })

export async function loadModel(url: string): Promise<LoadedModel> {
  const ext = url.split('?')[0].split('.').pop()?.toLowerCase()
  let object: THREE.Object3D
  let animations: THREE.AnimationClip[] = []
  switch (ext) {
    case 'glb':
    case 'gltf': {
      const gltf = await new GLTFLoader().loadAsync(url)
      object = gltf.scene
      animations = gltf.animations
      break
    }
    case 'obj': {
      object = await new OBJLoader().loadAsync(url)
      object.traverse((o) => {
        if (o instanceof THREE.Mesh) o.material = defaultMaterial()
      })
      break
    }
    case 'fbx': {
      object = await new FBXLoader().loadAsync(url)
      animations = object.animations
      break
    }
    case 'stl': {
      const geo = await new STLLoader().loadAsync(url)
      object = new THREE.Mesh(geo, defaultMaterial())
      break
    }
    case 'ply': {
      const geo = await new PLYLoader().loadAsync(url)
      geo.computeVertexNormals()
      const mat = defaultMaterial()
      if (geo.hasAttribute('color')) {
        mat.vertexColors = true
        mat.color.set(0xffffff)
      }
      object = new THREE.Mesh(geo, mat)
      break
    }
    default:
      throw new Error(`No 3D preview for .${ext} files`)
  }
  let triangles = 0
  object.traverse((o) => {
    if (o instanceof THREE.Mesh && o.geometry) {
      const g = o.geometry as THREE.BufferGeometry
      triangles += (g.index ? g.index.count : (g.getAttribute('position')?.count ?? 0)) / 3
    }
  })
  return { object, animations, triangles: Math.round(triangles) }
}

export interface Viewer {
  dispose: () => void
  snapshot: () => Promise<Blob | null>
}

export function createViewer(container: HTMLElement, model: LoadedModel, opts: { interactive?: boolean; size?: number } = {}): Viewer {
  const interactive = opts.interactive ?? true
  const renderer = new THREE.WebGLRenderer({ antialias: true, alpha: true, preserveDrawingBuffer: true })
  renderer.setPixelRatio(interactive ? Math.min(window.devicePixelRatio, 2) : 1)
  renderer.toneMapping = THREE.NeutralToneMapping // keeps base colours true
  renderer.outputColorSpace = THREE.SRGBColorSpace
  container.appendChild(renderer.domElement)
  renderer.domElement.style.width = '100%'
  renderer.domElement.style.height = '100%'
  renderer.domElement.style.touchAction = 'none'

  const scene = new THREE.Scene()
  const pmrem = new THREE.PMREMGenerator(renderer)
  scene.environment = pmrem.fromScene(new RoomEnvironment(), 0.04).texture
  const key = new THREE.DirectionalLight(0xffffff, 1.1)
  key.position.set(3, 5, 4)
  scene.add(key)
  scene.add(model.object)

  // Frame the model.
  const box = new THREE.Box3().setFromObject(model.object)
  const size = box.getSize(new THREE.Vector3())
  const center = box.getCenter(new THREE.Vector3())
  const radius = Math.max(size.length() / 2, 1e-4)
  const camera = new THREE.PerspectiveCamera(35, 1, radius / 100, radius * 100)
  const dist = (radius / Math.sin((camera.fov * Math.PI) / 360)) * 1.3
  camera.position.copy(center).add(new THREE.Vector3(0.9, 0.55, 1).normalize().multiplyScalar(dist))
  camera.lookAt(center)

  let controls: OrbitControls | undefined
  if (interactive) {
    controls = new OrbitControls(camera, renderer.domElement)
    controls.target.copy(center)
    controls.enableDamping = true
    controls.autoRotate = true
    controls.autoRotateSpeed = 1.2
    controls.addEventListener('start', () => controls && (controls.autoRotate = false))
    controls.update()
  }

  const mixer = model.animations.length ? new THREE.AnimationMixer(model.object) : undefined
  if (mixer) mixer.clipAction(model.animations[0]).play()

  const resize = () => {
    const w = opts.size ?? container.clientWidth
    const h = opts.size ?? container.clientHeight
    if (w === 0 || h === 0) return
    renderer.setSize(w, h, false)
    camera.aspect = w / h
    camera.updateProjectionMatrix()
  }
  resize()
  const ro = interactive ? new ResizeObserver(resize) : undefined
  ro?.observe(container)

  const timer = new THREE.Timer()
  let raf = 0
  let disposed = false
  const frame = () => {
    if (disposed) return
    timer.update()
    mixer?.update(timer.getDelta())
    controls?.update()
    renderer.render(scene, camera)
    if (interactive) raf = requestAnimationFrame(frame)
  }
  frame()

  return {
    snapshot: () =>
      new Promise((resolve) => {
        renderer.render(scene, camera)
        renderer.domElement.toBlob((b) => resolve(b), 'image/png')
      }),
    dispose: () => {
      disposed = true
      cancelAnimationFrame(raf)
      ro?.disconnect()
      controls?.dispose()
      scene.traverse((o) => {
        if (o instanceof THREE.Mesh) {
          o.geometry?.dispose()
          for (const m of Array.isArray(o.material) ? o.material : [o.material]) {
            for (const v of Object.values(m ?? {})) if (v instanceof THREE.Texture) v.dispose()
            m?.dispose?.()
          }
        }
      })
      pmrem.dispose()
      renderer.dispose()
      renderer.forceContextLoss()
      renderer.domElement.remove()
    },
  }
}

/** Renders a square thumbnail of a model off-screen. */
export async function renderModelThumb(url: string, size = 512): Promise<{ blob: Blob | null; triangles: number }> {
  const model = await loadModel(url)
  const host = document.createElement('div')
  host.style.cssText = `position:fixed;left:-9999px;top:0;width:${size}px;height:${size}px`
  document.body.appendChild(host)
  const viewer = createViewer(host, model, { interactive: false, size })
  try {
    // Give textures a moment to decode before the capture.
    await new Promise((r) => setTimeout(r, 250))
    return { blob: await viewer.snapshot(), triangles: model.triangles }
  } finally {
    viewer.dispose()
    host.remove()
  }
}
