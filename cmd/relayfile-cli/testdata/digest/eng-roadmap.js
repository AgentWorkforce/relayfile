module.exports = async function digest(ctx) {
  const events = await ctx.changeEvents({
    paths: ["/notion/databases/eng-roadmap/*"],
  });
  return {
    provider: "Eng Roadmap",
    bullets: events.map((event) => ({
      text: `${event.resource.id} changed`,
      canonicalPath: event.resource.path,
    })),
  };
};
