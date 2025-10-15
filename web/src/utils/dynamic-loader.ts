/**
 * 动态加载工具函数
 */

// CDN配置
const CDN_BASE = 'https://unpkg.com';

// 动态加载的库配置
const EXTERNAL_LIBS = {
  vchart: {
    url: `${CDN_BASE}/@visactor/vchart@latest/build/index.min.js`,
    global: 'VChart',
    check: () => (window as any).VChart
  },
  wangEditor: {
    url: `${CDN_BASE}/@wangeditor/editor@latest/dist/index.min.js`,
    global: 'wangEditor', 
    check: () => (window as any).wangEditor
  },
  xgplayer: {
    url: `${CDN_BASE}/xgplayer@latest/dist/index.min.js`,
    global: 'Player',
    check: () => (window as any).Player
  }
};

/**
 * 动态加载脚本
 */
function loadScript(url: string): Promise<void> {
  return new Promise((resolve, reject) => {
    if (document.querySelector(`script[src="${url}"]`)) {
      resolve();
      return;
    }

    const script = document.createElement('script');
    script.src = url;
    script.async = true;
    script.onload = () => resolve();
    script.onerror = () => reject(new Error(`Failed to load script: ${url}`));
    document.head.appendChild(script);
  });
}

/**
 * 加载外部库
 */
export async function loadExternalLib(libName: keyof typeof EXTERNAL_LIBS) {
  const lib = EXTERNAL_LIBS[libName];
  
  // 检查是否已加载
  if (lib.check()) {
    return lib.check();
  }

  try {
    await loadScript(lib.url);
    
    // 等待全局变量可用
    let attempts = 0;
    while (!lib.check() && attempts < 50) {
      await new Promise(resolve => setTimeout(resolve, 100));
      attempts++;
    }
    
    if (!lib.check()) {
      throw new Error(`Library ${libName} failed to initialize`);
    }
    
    return lib.check();
  } catch (error) {
    console.error(`Failed to load ${libName}:`, error);
    throw error;
  }
}

/**
 * 预加载资源
 */
export function preloadResource(url: string, as: 'script' | 'style' | 'image' = 'script') {
  if (document.querySelector(`link[href="${url}"]`)) {
    return;
  }

  const link = document.createElement('link');
  link.rel = 'preload';
  link.href = url;
  link.as = as;
  if (as === 'script') {
    link.crossOrigin = 'anonymous';
  }
  document.head.appendChild(link);
}

/**
 * 预获取资源（空闲时加载）
 */
export function prefetchResource(url: string) {
  if (document.querySelector(`link[href="${url}"]`)) {
    return;
  }

  const link = document.createElement('link');
  link.rel = 'prefetch';
  link.href = url;
  document.head.appendChild(link);
}

/**
 * 图片懒加载工具
 */
export function setupLazyImages() {
  if ('IntersectionObserver' in window) {
    const imageObserver = new IntersectionObserver((entries) => {
      entries.forEach(entry => {
        if (entry.isIntersecting) {
          const img = entry.target as HTMLImageElement;
          if (img.dataset.src) {
            img.src = img.dataset.src;
            img.removeAttribute('data-src');
            imageObserver.unobserve(img);
          }
        }
      });
    });

    document.querySelectorAll('img[data-src]').forEach(img => {
      imageObserver.observe(img);
    });
  }
}