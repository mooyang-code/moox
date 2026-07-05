<template>
  <div class="home-workbench">
    <section class="hero-card">
      <div>
        <p class="eyebrow">MooX Control Plane</p>
        <h1>{{ selectedSpaceId ? 'MooX 工作台' : '欢迎使用 MooX' }}</h1>
        <p class="hero-copy">
          {{ selectedSpaceId
            ? '从这里进入数据、采集、云节点和部署配置。'
            : '请先创建或选择一个空间。空间是数据、采集、交易和运维资源的业务隔离边界。' }}
        </p>
      </div>
      <div class="hero-actions">
        <a-button type="primary" @click="goSpaceSettings">
          {{ selectedSpaceId ? '切换/管理空间' : '创建空间' }}
        </a-button>
        <a-button v-if="selectedSpaceId" type="outline" @click="goServiceDeployments">
          服务部署
        </a-button>
      </div>
    </section>

    <a-alert v-if="!selectedSpaceId" class="space-alert" type="info" title="还没有选中空间">
      创建空间后，再配置数据源、数据集、采集规则和云节点。系统运行数据可以按 examples/e2e 流程重建。
    </a-alert>

    <section class="quick-grid">
      <a-card
        v-for="item in quickEntries"
        :key="item.path"
        class="quick-card"
        hoverable
        @click="go(item.path)"
      >
        <div class="quick-index">{{ item.index }}</div>
        <div>
          <h3>{{ item.title }}</h3>
          <p>{{ item.description }}</p>
        </div>
      </a-card>
    </section>

    <section class="flow-card">
      <div class="section-heading">
        <p class="eyebrow">Recommended setup flow</p>
        <h2>从空库重建系统数据</h2>
      </div>
      <div class="flow-list">
        <div v-for="step in setupSteps" :key="step.title" class="flow-step">
          <span class="flow-dot"></span>
          <div>
            <h4>{{ step.title }}</h4>
            <p>{{ step.description }}</p>
          </div>
        </div>
      </div>
    </section>
  </div>
</template>

<script setup lang="ts" name="Home">
import { computed } from 'vue';
import { storeToRefs } from 'pinia';
import { useRouter } from 'vue-router';
import { useSpaceStore } from '@/store/modules/space';

const router = useRouter();
const spaceStore = useSpaceStore();
const { selectedSpaceId } = storeToRefs(spaceStore);

const quickEntries = computed(() => [
  {
    index: '01',
    title: '数据资产',
    description: '维护数据源、Subject、Dataset、字段和视图元数据。',
    path: '/data/datasets',
  },
  {
    index: '02',
    title: '视图浏览',
    description: '查询已经构建的 View，检查 K 线等数据是否写入成功。',
    path: '/data/view-browse',
  },
  {
    index: '03',
    title: '采集规则',
    description: '根据数据集对象生成采集任务，并提交给云节点执行。',
    path: '/collector/rules',
  },
  {
    index: '04',
    title: '云节点',
    description: '管理云账户、SCF 节点和采集运行时代码包。',
    path: '/collector/functions',
  },
  {
    index: '05',
    title: '服务部署',
    description: '查看 admin、cloudnode、collector、storage 等独立服务地址。',
    path: '/settings/service-deployments',
  },
  {
    index: '06',
    title: '存储拓扑',
    description: '维护 storage primary/access/view/archive 的部署与路由。',
    path: '/ops/storage/nodes',
  },
]);

const setupSteps = [
  {
    title: '1. 选择空间',
    description: '空间是所有管理台请求的上下文，也是数据资产和采集配置的隔离边界。',
  },
  {
    title: '2. 配置服务部署',
    description: 'admin 只做管理台入口和网关转发；cloudnode、collector、storage 独立部署。',
  },
  {
    title: '3. 初始化数据资产',
    description: '从 examples/e2e 导入数据源、Subject、Dataset、字段和 View 定义。',
  },
  {
    title: '4. 生成采集任务',
    description: 'collector 根据 DatasetSubject 生成任务实例，并通过 cloudnode 下发到 SCF。',
  },
];

const go = (path: string) => {
  router.push(path);
};

const goSpaceSettings = () => {
  go('/settings/spaces');
};

const goServiceDeployments = () => {
  go('/settings/service-deployments');
};
</script>

<style lang="scss" scoped>
.home-workbench {
  display: flex;
  flex-direction: column;
  gap: 18px;
}

.hero-card,
.flow-card {
  position: relative;
  overflow: hidden;
  padding: 28px;
  border: 1px solid rgba(20, 43, 82, 8%);
  border-radius: 22px;
  background:
    radial-gradient(circle at top right, rgba(20, 118, 255, 18%), transparent 34%),
    linear-gradient(135deg, #f8fbff 0%, #eef5ff 48%, #f7f2e9 100%);
  box-shadow: 0 18px 45px rgba(29, 54, 91, 8%);
}

.hero-card {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 24px;
}

.eyebrow {
  margin: 0 0 8px;
  font-size: 12px;
  font-weight: 700;
  letter-spacing: 0.12em;
  text-transform: uppercase;
  color: #37618f;
}

h1,
h2,
h3,
h4,
p {
  margin: 0;
}

h1 {
  font-size: clamp(28px, 4vw, 44px);
  line-height: 1.08;
  color: #142136;
}

.hero-copy {
  max-width: 720px;
  margin-top: 14px;
  font-size: 15px;
  line-height: 1.8;
  color: #4e6178;
}

.hero-actions {
  display: flex;
  flex-shrink: 0;
  gap: 12px;
}

.space-alert {
  border-radius: 14px;
}

.quick-grid {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 16px;
}

.quick-card {
  cursor: pointer;
  border-radius: 18px;

  :deep(.arco-card-body) {
    display: flex;
    min-height: 132px;
    gap: 18px;
    padding: 22px;
  }

  h3 {
    font-size: 18px;
    color: #182235;
  }

  p {
    margin-top: 10px;
    line-height: 1.7;
    color: #657489;
  }
}

.quick-index {
  font-family: Georgia, "Times New Roman", serif;
  font-size: 28px;
  font-weight: 700;
  color: #2b74d7;
}

.section-heading {
  margin-bottom: 18px;

  h2 {
    font-size: 24px;
    color: #17233c;
  }
}

.flow-list {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 16px;
}

.flow-step {
  display: flex;
  gap: 12px;
  padding: 16px;
  border: 1px solid rgba(54, 97, 143, 12%);
  border-radius: 16px;
  background: rgba(255, 255, 255, 62%);

  h4 {
    font-size: 15px;
    color: #1d2a3d;
  }

  p {
    margin-top: 6px;
    line-height: 1.7;
    color: #607086;
  }
}

.flow-dot {
  width: 10px;
  height: 10px;
  margin-top: 6px;
  border-radius: 999px;
  background: #2b74d7;
  box-shadow: 0 0 0 6px rgba(43, 116, 215, 12%);
}

@media (max-width: 1080px) {
  .quick-grid,
  .flow-list {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}

@media (max-width: 720px) {
  .hero-card {
    flex-direction: column;
  }

  .hero-actions {
    width: 100%;
    flex-wrap: wrap;
  }

  .quick-grid,
  .flow-list {
    grid-template-columns: 1fr;
  }
}
</style>
