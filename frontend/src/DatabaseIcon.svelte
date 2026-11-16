<script lang="ts">
  let { provider }: { provider: string } = $props();
  const cutoutId = $props.id();
</script>

<svg viewBox="0 0 26 24" aria-hidden="true" width="22" height="22">
  <defs>
    <mask id={cutoutId}>
      <rect width="26" height="24" fill="white" />
      {#if provider === 'sqlite'}
        <path class="cutout" d="m8 17 9-10M11.8 12.8l-.3-4M14.6 9.8l-.1-3M12 12.6l4.1.1" />
      {:else}
        <circle cx="16.2" cy="8" r=".8" fill="black" />
        <path class="cutout" d="M12 6c-3 0-3 5 0 6" />
      {/if}
    </mask>
  </defs>
  <g mask={`url(#${cutoutId})`}>
  {#if provider === 'sqlite'}
    <path d="M22 2.5c-6.4-.2-11.8 2.8-13.8 8.1-.7 1.8-.9 3.6-.7 5.2L3.8 20a1 1 0 0 0 1.4 1.4l3.7-4.2c1.7.1 3.4-.2 5.1-1 5.2-2.4 8-7.8 8.5-12.8.1-.5-.1-.9-.5-.9Z" />
  {:else}
    <path d="M4 8a5 5 0 0 1 5-5h5a5 5 0 0 1 5 5v6a1.5 1.5 0 0 0 3 0v-2h2v2a3.5 3.5 0 0 1-7 0v-1h-1v7h-3v-5H8v5H5V10H4v5H2v-5a2 2 0 0 1 2-2Z" />
  {/if}
  </g>
</svg>

<style>
  svg { display: block; width: 22px; height: 22px; color: var(--muted, #666666); fill: currentColor; }
  .cutout { fill: none; stroke: black; stroke-width: 1.3; stroke-linecap: round; stroke-linejoin: round; }
</style>
