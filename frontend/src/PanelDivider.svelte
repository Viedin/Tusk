<script lang="ts">
  let { label, orientation, value, min, max, reverse = false, onresize }: {
    label: string;
    orientation: 'vertical' | 'horizontal';
    value: number;
    min: number;
    max: number;
    reverse?: boolean;
    onresize: (value: number) => void;
  } = $props();

  let drag: { pointerId: number; start: number; value: number } | null = null;
  const resize = (next: number) => onresize(Math.round(Math.max(min, Math.min(max, next))));

  function start(event: PointerEvent): void {
    if (event.button !== 0) return;
    event.preventDefault();
    drag = { pointerId: event.pointerId, start: orientation === 'vertical' ? event.clientX : event.clientY, value };
    const handle = event.currentTarget as HTMLElement;
    handle.focus({ preventScroll: true });
    handle.setPointerCapture(event.pointerId);
  }

  function move(event: PointerEvent): void {
    if (!drag || drag.pointerId !== event.pointerId) return;
    const position = orientation === 'vertical' ? event.clientX : event.clientY;
    resize(drag.value + (position - drag.start) * (reverse ? -1 : 1));
  }

  function key(event: KeyboardEvent): void {
    const backward = orientation === 'vertical' ? 'ArrowLeft' : 'ArrowUp';
    const forward = orientation === 'vertical' ? 'ArrowRight' : 'ArrowDown';
    if (event.key === 'Home') resize(min);
    else if (event.key === 'End') resize(max);
    else if (event.key === backward || event.key === forward) {
      resize(value + (event.key === backward ? -1 : 1) * (reverse ? -1 : 1) * (event.shiftKey ? 50 : 10));
    } else return;
    event.preventDefault();
  }
</script>

<!-- svelte-ignore a11y_no_noninteractive_tabindex, a11y_no_noninteractive_element_interactions (This ARIA separator is an interactive split-pane handle.) -->
<div tabindex="0" class="panel-divider" class:vertical={orientation === 'vertical'} class:horizontal={orientation === 'horizontal'} role="separator" aria-label={label} aria-orientation={orientation} aria-valuenow={Math.round(value)} aria-valuemin={min} aria-valuemax={max} title={`${label}. Drag or use arrow keys.`} onpointerdown={start} onpointermove={move} onpointerup={() => { drag = null; }} onpointercancel={() => { drag = null; }} onlostpointercapture={() => { drag = null; }} onkeydown={key}></div>
