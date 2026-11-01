import { definePlugin } from "../../types";

// The launcher has no page of its own: its Infrastructure section is a panel on Apps and the
// overview (slot contributions arrive with the panel slots).
export default definePlugin({ id: "launcher", order: 90, topics: {} });
