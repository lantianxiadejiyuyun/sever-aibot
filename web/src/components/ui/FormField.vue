<script setup lang="ts">
withDefaults(
  defineProps<{
    label: string
    forId: string
    required?: boolean
    hint?: string
    error?: string
    span2?: boolean
  }>(),
  { required: false, hint: '', error: '', span2: false },
)
</script>

<template>
  <div class="form-field" :class="{ 'span-2': span2 }">
    <label class="form-label" :for="forId">
      {{ label }}<span v-if="required" class="required" aria-hidden="true">*</span>
    </label>
    <slot :id="forId" />
    <p v-if="error" class="form-error" role="alert">{{ error }}</p>
    <p v-else-if="hint" class="form-hint">{{ hint }}</p>
  </div>
</template>
