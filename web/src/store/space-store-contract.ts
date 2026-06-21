import { useSpaceStore } from './modules/space';

export async function assertSpaceStoreContract() {
  const store = useSpaceStore();
  await store.loadSpaces();
  store.setSelectedSpace('contract-space');
  const selectedSpaceId: string = store.requireSpaceId();
  const spaceCount: number = store.spaces.length;
  const selectedName: string | undefined = store.selectedSpace?.name;

  return { selectedSpaceId, spaceCount, selectedName };
}
