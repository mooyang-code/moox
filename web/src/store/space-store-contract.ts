import { useSpaceStore } from "./modules/space";

export async function assertSpaceStoreContract() {
  const store = useSpaceStore();
  await store.loadSpaces();
  const requestedSpaceId = store.spaces[0]?.space_id || "contract-space";
  if (store.hasSpace(requestedSpaceId)) store.setSelectedSpace(requestedSpaceId);
  const selectedSpaceId: string = store.requireSpaceId();
  const spaceCount: number = store.spaces.length;
  const selectedName: string | undefined = store.selectedSpace?.name;
  const hasSelectedSpace: boolean = store.hasSpace(selectedSpaceId);

  return { selectedSpaceId, spaceCount, selectedName, hasSelectedSpace };
}
