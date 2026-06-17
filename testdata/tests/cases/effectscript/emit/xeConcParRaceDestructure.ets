// par over three effects with a destructured result + a two-element race (SPEC §8)

declare const ea: any, eb: any, ec: any;

effect xeConcParRaceDestructure() {
  [first, second, third] <- par [ea, eb, ec]
  { left, right } <- par { left: ea, right: eb }
  winner <- race [ea, eb]
  return [first, second, third, left, right, winner]
}
