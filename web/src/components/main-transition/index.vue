<template>
  <Transition name="fadeInOut" mode="out-in" appear>
    <!-- 向内传递插槽内容 -->
    <slot></slot>
  </Transition>
</template>
