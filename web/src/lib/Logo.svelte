<script lang="ts">
  // The slskdpp mark: a songbird calling out, for a client that seeks.
  let { size = 32 }: { size?: number } = $props();
  const id = $props.id();
</script>

<svg width={size} height={size} viewBox="0 0 64 64" role="img" aria-label="slskdpp">
  <defs>
    <linearGradient id="{id}-g" x1="0" y1="0" x2="1" y2="1">
      <stop offset="0" stop-color="#5b7cff" />
      <stop offset="1" stop-color="#4338ca" />
    </linearGradient>
  </defs>
  <rect width="64" height="64" rx="15" fill="url(#{id}-g)" />
  <path d="M18 40.5 L7 41.5 L10.5 48.5 L20 46 Z" fill="#fff" />
  <path
    d="M13 36.5 C13 27 20 20.5 29 20.5 C35.5 20.5 40 24.5 41 29.5 L48.5 32.5 L41 35 C40 44 33.5 49.5 25 49.5 C18 49.5 13 44.5 13 36.5 Z"
    fill="#fff"
  />
  <path d="M15.5 36 C20 31.5 28.5 31.5 34 38.5 C28 44 19.5 43.5 15.5 36 Z" fill="#c7cffd" />
  <circle cx="33.5" cy="29" r="2.3" fill="#3f3fc4" />
  <path d="M52.5 26.5 A7 7 0 0 1 52.5 38.5" fill="none" stroke="#fff" stroke-width="3" stroke-linecap="round" opacity=".9" />
</svg>
