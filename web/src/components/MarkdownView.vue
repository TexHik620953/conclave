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
.md { line-height: 1.62; word-wrap: break-word; }
.md > :first-child { margin-top: 0; }
.md > :last-child { margin-bottom: 0; }
.md h1, .md h2, .md h3, .md h4 { margin: 0.9em 0 0.45em; line-height: 1.3; letter-spacing: -0.01em; }
.md h1 { font-size: 1.42em; }
.md h2 { font-size: 1.22em; }
.md h3 { font-size: 1.06em; }
.md p { margin: 0.5em 0; }
.md ul, .md ol { margin: 0.45em 0; padding-left: 1.4em; }
.md li { margin: 0.2em 0; }
.md a { color: var(--accent); text-decoration: none; }
.md a:hover { text-decoration: underline; }
.md code {
  background: var(--panel-3); border: 1px solid var(--border); border-radius: 5px;
  padding: 1px 5px; font-family: var(--mono); font-size: 0.87em;
}
.md pre {
  background: var(--bg-2); border: 1px solid var(--border); border-radius: 10px;
  padding: 12px 14px; overflow-x: auto; margin: 0.6em 0;
}
.md pre code { background: none; border: none; padding: 0; }
.md blockquote {
  margin: 0.6em 0; padding: 0.3em 0.9em; border-left: 3px solid var(--accent);
  background: var(--accent-soft); border-radius: 0 8px 8px 0; color: var(--text);
}
.md table { border-collapse: collapse; margin: 0.6em 0; width: 100%; font-size: 0.94em; }
.md th, .md td { border: 1px solid var(--border); padding: 6px 10px; text-align: left; }
.md th { background: var(--panel-2); }
.md hr { border: none; border-top: 1px solid var(--border); margin: 1em 0; }
.md img { max-width: 100%; border-radius: 8px; }
</style>
