<script>
  // 内联 SVG 图标。不引图标库：这里只需要十来个，内联能跟着文字颜色走，
  // 也省一次网络请求。
  let { name, size = 16, stroke = 1.8 } = $props();

  const paths = {
    plus: 'M12 5v14M5 12h14',
    menu: 'M3 6h18M3 12h18M3 18h18',
    panel: 'M4 5h16v14H4zM9 5v14',
    send: 'M12 19V5M5 12l7-7 7 7',
    stop: 'M7 7h10v10H7z',
    trash: 'M4 7h16M10 11v6M14 11v6M6 7l1 13h10l1-13M9 7V4h6v3',
    pencil: 'M4 20h4l10-10-4-4L4 16v4zM14 6l4 4',
    check: 'M5 13l4 4L19 7',
    close: 'M6 6l12 12M18 6L6 18',
    copy: 'M9 9h10v10H9zM5 15V5h10',
    chevronRight: 'M9 6l6 6-6 6',
    chevronDown: 'M6 9l6 6 6-6',
    sparkle: 'M12 3l2 6 6 2-6 2-2 6-2-6-6-2 6-2z',
    clock: 'M12 7v5l3 2M21 12a9 9 0 11-18 0 9 9 0 0118 0z',
    search: 'M11 19a8 8 0 100-16 8 8 0 000 16zM21 21l-4.3-4.3',
    brain: 'M12 5a3 3 0 00-3 3v8a3 3 0 003 3M12 5a3 3 0 013 3v8a3 3 0 01-3 3M9 9H7a2 2 0 000 4h2M15 9h2a2 2 0 010 4h-2',
    tool: 'M14.7 6.3a4 4 0 11-5.4 5.4L4 17v3h3l5.3-5.3a4 4 0 015.4-5.4l-2.6 2.6-1.4-1.4 2.6-2.6z',
    alert: 'M12 8v5M12 16v.5M10.3 4.3L3 17a2 2 0 001.7 3h14.6a2 2 0 001.7-3L13.7 4.3a2 2 0 00-3.4 0z',
    chat: 'M21 12a8 8 0 01-8 8H7l-4 3 1.2-4.2A8 8 0 1121 12z',
    // 能力概览与分组导航用
    book: 'M4 5a2 2 0 012-2h12v18H6a2 2 0 01-2-2zM8 7h8M8 11h6',
    layers: 'M12 3l9 5-9 5-9-5 9-5zM3 12l9 5 9-5M3 16l9 5 9-5',
    route: 'M6 4v5a3 3 0 003 3h6a3 3 0 013 3v5M6 4h.01M18 20h.01M6 4a2 2 0 100 4 2 2 0 000-4z',
    database: 'M12 3c4.4 0 8 1.3 8 3s-3.6 3-8 3-8-1.3-8-3 3.6-3 8-3zM4 6v12c0 1.7 3.6 3 8 3s8-1.3 8-3V6M4 12c0 1.7 3.6 3 8 3s8-1.3 8-3',
    chart: 'M4 20V10M10 20V4M16 20v-7M22 20H2',
    shield: 'M12 3l8 3v6c0 5-3.4 8.2-8 9-4.6-.8-8-4-8-9V6l8-3zM9 12l2 2 4-4',
    info: 'M12 21a9 9 0 100-18 9 9 0 000 18zM12 11v6M12 7.5v.5',
    help: 'M12 21a9 9 0 100-18 9 9 0 000 18zM9.5 9a2.5 2.5 0 115 0c0 1.5-2.5 2-2.5 4M12 17v.5',
    refresh: 'M20 11a8 8 0 10-2.3 6.3M20 5v6h-6',
    link: 'M10 13a5 5 0 007.1 0l2-2a5 5 0 00-7.1-7.1l-1 1M14 11a5 5 0 00-7.1 0l-2 2A5 5 0 0012 20.1l1-1',
    archive: 'M3 5h18v4H3zM5 9v11h14V9M10 13h4',
  };
</script>

<svg
  class="icon block flex-none"
  width={size}
  height={size}
  viewBox="0 0 24 24"
  fill="none"
  stroke="currentColor"
  stroke-width={stroke}
  stroke-linecap="round"
  stroke-linejoin="round"
  aria-hidden="true"
>
  <path d={paths[name] || ''} />
</svg>
