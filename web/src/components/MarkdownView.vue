<script setup lang="ts">
import { computed } from 'vue'
import { marked } from 'marked'
import DOMPurify from 'dompurify'

const props = defineProps<{ text: string }>()

marked.setOptions({ gfm: true, breaks: true })

const html = computed(() => {
  const raw = marked.parse(props.text || '', { async: false }) as string
  return DOMPurify.sanitize(raw)
})
</script>

<template>
  <div class="md" v-html="html" />
</template>

<style>
.md { line-height: 1.55; word-wrap: break-word; }
.md h1, .md h2, .md h3, .md h4 { margin: 0.7em 0 0.4em; line-height: 1.25; }
.md h1 { font-size: 1.4em; }
.md h2 { font-size: 1.2em; }
.md h3 { font-size: 1.05em; }
.md p { margin: 0.45em 0; }
.md ul, .md ol { margin: 0.4em 0; padding-left: 1.4em; }
.md li { margin: 0.15em 0; }
.md a { color: var(--accent); }
.md code {
  background: var(--panel-2); border: 1px solid var(--border); border-radius: 4px;
  padding: 0 4px; font-family: ui-monospace, Menlo, monospace; font-size: 0.9em;
}
.md pre {
  background: var(--panel-2); border: 1px solid var(--border); border-radius: 6px;
  padding: 10px 12px; overflow-x: auto; margin: 0.5em 0;
}
.md pre code { background: none; border: none; padding: 0; }
.md blockquote {
  margin: 0.5em 0; padding: 0.2em 0.8em; border-left: 3px solid var(--border); color: var(--muted);
}
.md table { border-collapse: collapse; margin: 0.5em 0; }
.md th, .md td { border: 1px solid var(--border); padding: 4px 8px; }
.md hr { border: none; border-top: 1px solid var(--border); margin: 0.8em 0; }
.md img { max-width: 100%; }
</style>
